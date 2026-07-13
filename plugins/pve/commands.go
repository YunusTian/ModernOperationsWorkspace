// commands.go —— PVE 只读命令实现（cluster.status / node.list / vm.list /
// vm.status / lxc.list）。
//
// 输入约定：
//   - 所有命令都可选 "endpoint" 字段选择目标；空则使用 settings.endpoints[0]。
//   - 幂等命令 (Idempotent=true) 用于 core/command 的缓存/重试策略。
//
// 输出约定：
//   - JSON 结构由本文件中显式定义，与 PVE 原始响应解耦；PVE 版本升级只需
//     调整解析层，Command 契约不动。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/mow/mow/sdk"
)

// commonParams 是所有 PVE 命令共用的目标定位。
type commonParams struct {
	Endpoint string `json:"endpoint,omitempty"`
}

// -----------------------------------------------------------------------------
// cluster.status
// -----------------------------------------------------------------------------

type clusterStatusCmd struct{ p *PVEPlugin }

func (c *clusterStatusCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID: "cluster.status", Description: "cluster status summary",
		Permission: sdk.PermRead, ConnectionType: "pve", Idempotent: true,
	}
}

func (c *clusterStatusCmd) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}

// clusterEntry 对齐 /cluster/status 中的一行；PVE 每行有 type=cluster/node/…
type clusterEntry struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	NodeID  int    `json:"nodeid,omitempty"`
	Nodes   int    `json:"nodes,omitempty"`
	Quorate int    `json:"quorate,omitempty"`
	Online  int    `json:"online,omitempty"`
	Level   string `json:"level,omitempty"`
	IP      string `json:"ip,omitempty"`
	Local   int    `json:"local,omitempty"`
}

type clusterStatusResult struct {
	Entries []clusterEntry `json:"entries"`
}

func (c *clusterStatusCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	var p commonParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	cli, err := c.p.clientFor(p.Endpoint)
	if err != nil {
		return nil, err
	}
	var entries []clusterEntry
	if err := cli.getJSON(ctx, "/cluster/status", nil, &entries); err != nil {
		return nil, err
	}
	return marshalResp(clusterStatusResult{Entries: entries})
}

// -----------------------------------------------------------------------------
// node.list
// -----------------------------------------------------------------------------

type nodeListCmd struct{ p *PVEPlugin }

func (c *nodeListCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID: "node.list", Description: "list cluster nodes",
		Permission: sdk.PermRead, ConnectionType: "pve", Idempotent: true,
	}
}
func (c *nodeListCmd) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}

type nodeEntry struct {
	Node    string  `json:"node"`
	Status  string  `json:"status,omitempty"`
	CPU     float64 `json:"cpu,omitempty"`
	MaxCPU  int     `json:"maxcpu,omitempty"`
	Mem     int64   `json:"mem,omitempty"`
	MaxMem  int64   `json:"maxmem,omitempty"`
	Disk    int64   `json:"disk,omitempty"`
	MaxDisk int64   `json:"maxdisk,omitempty"`
	Uptime  int64   `json:"uptime,omitempty"`
	Level   string  `json:"level,omitempty"`
}

type nodeListResult struct {
	Nodes []nodeEntry `json:"nodes"`
}

func (c *nodeListCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	var p commonParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	cli, err := c.p.clientFor(p.Endpoint)
	if err != nil {
		return nil, err
	}
	var nodes []nodeEntry
	if err := cli.getJSON(ctx, "/nodes", nil, &nodes); err != nil {
		return nil, err
	}
	return marshalResp(nodeListResult{Nodes: nodes})
}

// -----------------------------------------------------------------------------
// vm.list / lxc.list —— guest 列表
// -----------------------------------------------------------------------------

// guestListParams 允许限定 node，不填时聚合整个 cluster。
type guestListParams struct {
	commonParams
	Node string `json:"node,omitempty"`
}

type guestEntry struct {
	Node    string  `json:"node"`
	VMID    int     `json:"vmid"`
	Name    string  `json:"name,omitempty"`
	Status  string  `json:"status,omitempty"`
	CPU     float64 `json:"cpu,omitempty"`
	Mem     int64   `json:"mem,omitempty"`
	MaxMem  int64   `json:"maxmem,omitempty"`
	Disk    int64   `json:"disk,omitempty"`
	MaxDisk int64   `json:"maxdisk,omitempty"`
	Uptime  int64   `json:"uptime,omitempty"`
	Tags    string  `json:"tags,omitempty"`
	Type    string  `json:"type,omitempty"`
}

type vmListResult struct {
	VMs []guestEntry `json:"vms"`
}

type lxcListResult struct {
	LXCs []guestEntry `json:"lxcs"`
}

type vmListCmd struct{ p *PVEPlugin }

func (c *vmListCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID: "vm.list", Description: "list QEMU VMs",
		Permission: sdk.PermRead, ConnectionType: "pve", Idempotent: true,
	}
}
func (c *vmListCmd) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *vmListCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	entries, err := listGuests(ctx, c.p, req, "qemu")
	if err != nil {
		return nil, err
	}
	return marshalResp(vmListResult{VMs: entries})
}

type lxcListCmd struct{ p *PVEPlugin }

func (c *lxcListCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID: "lxc.list", Description: "list LXC containers",
		Permission: sdk.PermRead, ConnectionType: "pve", Idempotent: true,
	}
}
func (c *lxcListCmd) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *lxcListCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	entries, err := listGuests(ctx, c.p, req, "lxc")
	if err != nil {
		return nil, err
	}
	return marshalResp(lxcListResult{LXCs: entries})
}

// listGuests 是 vm.list 与 lxc.list 的共同实现。
//   - Node 为空：走 /cluster/resources?type=<kind>（一次拿全集群，最快）
//   - Node 指定：走 /nodes/<node>/{qemu|lxc}（明确 host 归属）
//
// 输出的 Type 字段总是被强制填成 kind（"qemu" / "lxc"），便于 UI 侧过滤。
func listGuests(ctx context.Context, p *PVEPlugin, req *sdk.ExecuteRequest, kind string) ([]guestEntry, error) {
	var params guestListParams
	if err := decodeParams(req.Params, &params); err != nil {
		return nil, err
	}
	cli, err := p.clientFor(params.Endpoint)
	if err != nil {
		return nil, err
	}
	var entries []guestEntry
	if params.Node == "" {
		q := url.Values{}
		q.Set("type", kind)
		if err := cli.getJSON(ctx, "/cluster/resources", q, &entries); err != nil {
			return nil, err
		}
	} else {
		var subPath string
		if kind == "qemu" {
			subPath = "/nodes/" + params.Node + "/qemu"
		} else {
			subPath = "/nodes/" + params.Node + "/lxc"
		}
		var raw []guestEntry
		if err := cli.getJSON(ctx, subPath, nil, &raw); err != nil {
			return nil, err
		}
		// /nodes/<node>/qemu 返回项不含 Node 字段，补齐它便于上层聚合。
		for i := range raw {
			if raw[i].Node == "" {
				raw[i].Node = params.Node
			}
		}
		entries = raw
	}
	for i := range entries {
		entries[i].Type = kind
	}
	return entries, nil
}

// -----------------------------------------------------------------------------
// vm.status —— 单个 QEMU VM 的 status/current
// -----------------------------------------------------------------------------

type vmStatusParams struct {
	commonParams
	Node string `json:"node"`
	VMID int    `json:"vmid"`
}

// vmStatusResult 只透出常用字段；额外字段用 Extra 存放，避免 PVE 版本升级时漏字段。
type vmStatusResult struct {
	VMID    int     `json:"vmid"`
	Name    string  `json:"name,omitempty"`
	Status  string  `json:"status,omitempty"`
	Qmpstat string  `json:"qmpstatus,omitempty"`
	CPU     float64 `json:"cpu,omitempty"`
	CPUs    int     `json:"cpus,omitempty"`
	Mem     int64   `json:"mem,omitempty"`
	MaxMem  int64   `json:"maxmem,omitempty"`
	Uptime  int64   `json:"uptime,omitempty"`
	HA      bool    `json:"ha,omitempty"`
	Extra   json.RawMessage `json:"extra,omitempty"`
}

type vmStatusCmd struct{ p *PVEPlugin }

func (c *vmStatusCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID: "vm.status", Description: "QEMU VM status detail",
		Permission: sdk.PermRead, ConnectionType: "pve", Idempotent: true,
	}
}
func (c *vmStatusCmd) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *vmStatusCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	var p vmStatusParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.Node == "" || p.VMID == 0 {
		return nil, sdk.NewError("PARAM_INVALID", "vm.status requires 'node' and 'vmid'", nil)
	}
	cli, err := c.p.clientFor(p.Endpoint)
	if err != nil {
		return nil, err
	}
	// 用 map[string]any 保留全部字段
	var rawMap map[string]any
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/current", p.Node, p.VMID)
	if err := cli.getJSON(ctx, path, nil, &rawMap); err != nil {
		return nil, err
	}
	result := vmStatusResult{VMID: p.VMID}
	if v, ok := rawMap["name"].(string); ok {
		result.Name = v
	}
	if v, ok := rawMap["status"].(string); ok {
		result.Status = v
	}
	if v, ok := rawMap["qmpstatus"].(string); ok {
		result.Qmpstat = v
	}
	if v, ok := rawMap["cpu"].(float64); ok {
		result.CPU = v
	}
	if v, ok := rawMap["cpus"].(float64); ok {
		result.CPUs = int(v)
	}
	if v, ok := rawMap["mem"].(float64); ok {
		result.Mem = int64(v)
	}
	if v, ok := rawMap["maxmem"].(float64); ok {
		result.MaxMem = int64(v)
	}
	if v, ok := rawMap["uptime"].(float64); ok {
		result.Uptime = int64(v)
	}
	if v, ok := rawMap["ha"].(map[string]any); ok {
		if managed, ok := v["managed"].(float64); ok {
			result.HA = managed != 0
		}
	}
	extraBytes, err := json.Marshal(rawMap)
	if err == nil {
		result.Extra = extraBytes
	}
	return marshalResp(result)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// clientFor 走 PVEPlugin.resolveEndpoint → newHTTPClient。
// 命令层不需要缓存 client：每次 request 都 short-lived 更安全，且 http.Client 本身就有连接池。
func (p *PVEPlugin) clientFor(name string) (*httpClient, error) {
	ep, err := p.resolveEndpoint(name)
	if err != nil {
		return nil, sdk.NewError("PVE_ENDPOINT_MISSING", err.Error(), nil)
	}
	return newHTTPClient(ep), nil
}

// decodeParams 反序列化 Command 入参；nil 时视为空对象。
func decodeParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return sdk.NewError("PARAM_INVALID", err.Error(), err)
	}
	return nil
}

// marshalResp 把结构体编码为 sdk.ExecuteResponse。
func marshalResp(v any) (*sdk.ExecuteResponse, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}
