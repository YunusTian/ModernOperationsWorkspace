// dangerous.go —— v0.3 第三阶段：docker.rm。
//
// 与 start/stop/restart 的关键差异：
//   - Permission = Dangerous → 必须由调用方 Confirmed=true 才允许执行
//   - 结果**不可逆**：容器一旦移除，无法通过 restart 恢复；若 v=true 还会
//     连带删除匿名 volume
//
// Engine API：DELETE /containers/{id}?force=<bool>&v=<bool>&link=<bool>
//   - force：容器在运行时强制杀掉（等价于 kill 后 rm）
//   - v：一并删除匿名 volume
//   - link：删除指定 link（我们**不暴露**，避免误伤）

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/mow/mow/sdk"
)

type rmParams struct {
	ID    string `json:"id"`
	Force bool   `json:"force,omitempty"`
	// Volumes：连带删除匿名 volume。默认 false —— 命名 volume 永远不受影响。
	Volumes bool `json:"volumes,omitempty"`
}

type rmResult struct {
	ID string `json:"id"`
}

type rmCmd struct{}

func (c *rmCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "rm",
		Description:    "remove a Docker container (irreversible; requires confirmation)",
		Permission:     sdk.PermDangerous,
		ConnectionType: "docker",
	}
}
func (c *rmCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *rmCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if req.Connection == nil {
		return nil, sdk.ErrConnectionRequired
	}
	// Dangerous 权限：Core 层的 AllowConfirmer 会拒未确认调用；
	// 这里再做一次防御性判断，防止桌面 / SDK 侧 middleware 绕过。
	if !req.Confirmed {
		return nil, sdk.NewError("CONFIRMATION_REQUIRED",
			"docker.rm requires user confirmation", nil)
	}
	var p rmParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, sdk.NewError("PARAM_INVALID", "id is required", nil)
	}
	dt, err := resolveTarget(req.Connection)
	if err != nil {
		return nil, sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	cli, err := newEngineClient(dt)
	if err != nil {
		return nil, sdk.NewError("DOCKER_CLIENT_INVALID", err.Error(), err)
	}
	defer cli.closeIdle()

	q := url.Values{}
	if p.Force {
		q.Set("force", "true")
	}
	if p.Volumes {
		q.Set("v", "true")
	}
	resp, err := cli.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(p.ID), q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := json.Marshal(rmResult{ID: p.ID})
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}
