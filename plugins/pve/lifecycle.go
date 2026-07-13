// lifecycle.go —— PVE guest 生命周期命令：vm.{start,stop,reboot} 与
// lxc.{start,stop,reboot}。所有命令共用同一模式：POST 到 `/nodes/<node>/{qemu|lxc}/<vmid>/status/<action>`
// 并返回 PVE 的 task UPID。
//
// 语义与官方 API 对齐：
//   - start   → 若已经 running 会返回错误
//   - stop    → 我们默认调用 `/status/shutdown`（软关机）；调用方通过 `force=true`
//               走 `/status/stop`（硬停）；这与 v0.7 引入的 Dangerous stop 保持渐进兼容
//   - reboot  → 调用 `/status/reboot`
//
// 权限：sdk.PermExecute（不是 Dangerous；正式版可能会把 stop force=true 提到 Dangerous，
// 参考 development-plan §4.4 "复杂 stop / 删除 → v0.7"）
package main

import (
	"context"
	"fmt"
	"net/url"

	"github.com/mow/mow/sdk"
)

// lifecycleParams 是所有生命周期命令的入参。
type lifecycleParams struct {
	commonParams
	Node    string `json:"node"`
	VMID    int    `json:"vmid"`
	Force   bool   `json:"force,omitempty"`   // 只影响 stop：true → /status/stop，否则 /status/shutdown
	Timeout int    `json:"timeout,omitempty"` // 传给 PVE 的 timeout 参数（stop/shutdown 使用；秒）
}

// lifecycleResult 是所有生命周期命令的返回；UPID 是 PVE 的任务标识，
// UI 可以据此进入任务详情面板（v0.5.2 只透传，不做轮询）。
type lifecycleResult struct {
	UPID    string `json:"upid,omitempty"`
	Node    string `json:"node"`
	VMID    int    `json:"vmid"`
	Action  string `json:"action"`
	Kind    string `json:"kind"` // "qemu" / "lxc"
	Message string `json:"message,omitempty"`
}

// vmLifecycleCmd 覆盖 vm.start / vm.stop / vm.reboot 三条命令。
// action 由 registrar 在 Commands() 内确定，避免重复代码。
type vmLifecycleCmd struct {
	p      *PVEPlugin
	action string // "start" | "stop" | "reboot"
}

func (c *vmLifecycleCmd) Spec() sdk.CommandSpec {
	return lifecycleSpec("vm", c.action)
}
func (c *vmLifecycleCmd) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *vmLifecycleCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return execLifecycle(ctx, c.p, req, "qemu", c.action)
}

// lxcLifecycleCmd：LXC 三条命令的实现体，与 vmLifecycleCmd 共享 execLifecycle。
type lxcLifecycleCmd struct {
	p      *PVEPlugin
	action string
}

func (c *lxcLifecycleCmd) Spec() sdk.CommandSpec {
	return lifecycleSpec("lxc", c.action)
}
func (c *lxcLifecycleCmd) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *lxcLifecycleCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return execLifecycle(ctx, c.p, req, "lxc", c.action)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func lifecycleSpec(kind, action string) sdk.CommandSpec {
	desc := ""
	switch action {
	case "start":
		desc = "start guest (async task, returns PVE UPID)"
	case "stop":
		desc = "stop guest (default shutdown; pass force=true for hard stop)"
	case "reboot":
		desc = "reboot guest"
	}
	return sdk.CommandSpec{
		ID:             kind + "." + action,
		Description:    desc,
		Permission:     sdk.PermExecute,
		ConnectionType: "pve",
	}
}

func execLifecycle(ctx context.Context, p *PVEPlugin, req *sdk.ExecuteRequest, kind, action string) (*sdk.ExecuteResponse, error) {
	var params lifecycleParams
	if err := decodeParams(req.Params, &params); err != nil {
		return nil, err
	}
	if params.Node == "" || params.VMID == 0 {
		return nil, sdk.NewError("PARAM_INVALID", "lifecycle requires 'node' and 'vmid'", nil)
	}
	cli, err := p.clientFor(params.Endpoint)
	if err != nil {
		return nil, err
	}

	// PVE API URL 前缀：/nodes/<node>/qemu/<vmid>/status/<verb>
	// 对 stop：软 → shutdown；硬 → stop
	verb := action
	if action == "stop" && !params.Force {
		verb = "shutdown"
	}
	path := fmt.Sprintf("/nodes/%s/%s/%d/status/%s", params.Node, kind, params.VMID, verb)

	form := url.Values{}
	if params.Timeout > 0 && (verb == "shutdown" || verb == "stop") {
		form.Set("timeout", fmt.Sprintf("%d", params.Timeout))
	}

	var upid string
	if err := cli.post(ctx, path, form, &upid); err != nil {
		return nil, err
	}
	return marshalResp(lifecycleResult{
		UPID: upid, Node: params.Node, VMID: params.VMID,
		Action: action, Kind: kind,
	})
}
