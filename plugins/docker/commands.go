package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// docker.list —— 容器列表
// -----------------------------------------------------------------------------
//
// Docker Engine API：GET /containers/json?all=<bool>&limit=<n>&filters=<json>
//
// v0.3 MVP 只透传三个字段：all / limit / labels（labels → filters.label）。
// 更复杂的 filters 语法（key=value 数组、network、status）留待后续。

// listParams 是 docker.list 的入参。
type listParams struct {
	// All 为 true 时列出所有容器（包含 exited）。默认 false。
	All bool `json:"all,omitempty"`

	// Limit 限制返回条数；0 表示不限。
	Limit int `json:"limit,omitempty"`

	// Labels 是 {"key":"value"} 形式的过滤条件；空则不过滤。
	Labels map[string]string `json:"labels,omitempty"`
}

// listContainer 是 docker.list 返回的一行摘要。
type listContainer struct {
	ID      string            `json:"id"`
	Names   []string          `json:"names"`
	Image   string            `json:"image"`
	ImageID string            `json:"image_id,omitempty"`
	Command string            `json:"command,omitempty"`
	Created int64             `json:"created,omitempty"`
	State   string            `json:"state"`
	Status  string            `json:"status,omitempty"`
	Ports   []listPort        `json:"ports,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type listPort struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"private_port"`
	PublicPort  int    `json:"public_port,omitempty"`
	Type        string `json:"type,omitempty"`
}

// engineContainer 对齐 Docker Engine /containers/json 的字段（局部）。
type engineContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Command string            `json:"Command"`
	Created int64             `json:"Created"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Ports   []enginePort      `json:"Ports"`
	Labels  map[string]string `json:"Labels"`
}

type enginePort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type listResult struct {
	Containers []listContainer `json:"containers"`
}

// listCmd 是 docker.list 的实现。
// 权限：Read。
type listCmd struct{}

func (c *listCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "list",
		Description:    "list Docker containers on the target engine",
		Permission:     sdk.PermRead,
		ConnectionType: "docker",
		Idempotent:     true,
	}
}

func (c *listCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

func (c *listCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if req.Connection == nil {
		return nil, sdk.ErrConnectionRequired
	}
	var p listParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
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
	if p.All {
		q.Set("all", "true")
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if len(p.Labels) > 0 {
		filters := map[string][]string{"label": labelsToFilter(p.Labels)}
		raw, err := json.Marshal(filters)
		if err != nil {
			return nil, sdk.NewError("PARAM_INVALID", "encode label filter: "+err.Error(), err)
		}
		q.Set("filters", string(raw))
	}

	var engineList []engineContainer
	if err := cli.getJSON(ctx, "/containers/json", q, &engineList); err != nil {
		return nil, err
	}

	out := listResult{Containers: make([]listContainer, 0, len(engineList))}
	for _, ec := range engineList {
		out.Containers = append(out.Containers, listContainer{
			ID:      ec.ID,
			Names:   ec.Names,
			Image:   ec.Image,
			ImageID: ec.ImageID,
			Command: ec.Command,
			Created: ec.Created,
			State:   ec.State,
			Status:  ec.Status,
			Ports:   convertPorts(ec.Ports),
			Labels:  ec.Labels,
		})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

func convertPorts(in []enginePort) []listPort {
	if len(in) == 0 {
		return nil
	}
	out := make([]listPort, len(in))
	for i, p := range in {
		out[i] = listPort{
			IP:          p.IP,
			PrivatePort: p.PrivatePort,
			PublicPort:  p.PublicPort,
			Type:        p.Type,
		}
	}
	return out
}

func labelsToFilter(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v == "" {
			out = append(out, k)
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// -----------------------------------------------------------------------------
// docker.inspect —— 单个容器详情
// -----------------------------------------------------------------------------
//
// Docker Engine API：GET /containers/{id}/json
//
// 只透传原始 JSON，避免每次 Engine 版本升级都要跟着改字段。

type inspectParams struct {
	// ID 是容器 ID / 名字。必填。
	ID string `json:"id"`
	// Size 为 true 时附带 SizeRw / SizeRootFs（性能开销较大）。
	Size bool `json:"size,omitempty"`
}

type inspectCmd struct{}

func (c *inspectCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "inspect",
		Description:    "inspect a Docker container by id or name",
		Permission:     sdk.PermRead,
		ConnectionType: "docker",
		Idempotent:     true,
	}
}

func (c *inspectCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

func (c *inspectCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if req.Connection == nil {
		return nil, sdk.ErrConnectionRequired
	}
	var p inspectParams
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
	if p.Size {
		q.Set("size", "true")
	}
	resp, err := cli.do(ctx, http.MethodGet, "/containers/"+url.PathEscape(p.ID)+"/json", q)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 透传原始 JSON 作为 Data；后续 UI 可直接消费。
	raw, err := readAllLimited(resp.Body, 8<<20) // 8 MiB 上限，避免异常大响应
	if err != nil {
		return nil, sdk.NewError("DOCKER_READ_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: raw}, nil
}

// -----------------------------------------------------------------------------
// docker.start / docker.stop / docker.restart —— 生命周期
// -----------------------------------------------------------------------------
//
// 语义（对齐 Engine API）：
//   - start：POST /containers/{id}/start；304 = 已在运行，视为成功
//   - stop： POST /containers/{id}/stop[?t=<n>]
//   - restart：POST /containers/{id}/restart[?t=<n>]
//
// 权限：Execute（等价于用户远程 `docker start|stop|restart <id>`）。
// 高影响 / 不可逆的 rm / kill -9 / force 放到第二批 Dangerous 命令。

type lifecycleParams struct {
	ID string `json:"id"`

	// TimeoutSec 仅对 stop / restart 生效：等待容器优雅退出的秒数。
	// <= 0 时不传，走 Engine 默认（10s）。
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

type lifecycleResult struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	// AlreadyInState 为 true 时表示"目标状态已成立"（例：Engine 304 not modified）。
	AlreadyInState bool `json:"already_in_state,omitempty"`
}

type startCmd struct{}

func (c *startCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "start",
		Description:    "start a Docker container",
		Permission:     sdk.PermExecute,
		ConnectionType: "docker",
	}
}
func (c *startCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *startCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return runLifecycle(ctx, req, "start")
}

type stopCmd struct{}

func (c *stopCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "stop",
		Description:    "stop a running Docker container (SIGTERM + timeout, then SIGKILL)",
		Permission:     sdk.PermExecute,
		ConnectionType: "docker",
	}
}
func (c *stopCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *stopCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return runLifecycle(ctx, req, "stop")
}

type restartCmd struct{}

func (c *restartCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "restart",
		Description:    "restart a Docker container",
		Permission:     sdk.PermExecute,
		ConnectionType: "docker",
	}
}
func (c *restartCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}
func (c *restartCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return runLifecycle(ctx, req, "restart")
}

// runLifecycle 是 start/stop/restart 的公共实现。
func runLifecycle(ctx context.Context, req *sdk.ExecuteRequest, action string) (*sdk.ExecuteResponse, error) {
	if req.Connection == nil {
		return nil, sdk.ErrConnectionRequired
	}
	var p lifecycleParams
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
	if (action == "stop" || action == "restart") && p.TimeoutSec > 0 {
		q.Set("t", strconv.Itoa(p.TimeoutSec))
	}
	path := "/containers/" + url.PathEscape(p.ID) + "/" + action

	callErr := cli.postNoBody(ctx, path, q)
	already := false
	if callErr != nil {
		// Engine 用 304 表示"已经处于目标状态"（start 已运行 / stop 已停止）。
		// decodeEngineError 会把 304 打成 DOCKER_NOT_MODIFIED；我们视为成功。
		var sdkErr *sdk.Error
		if isSDKError(callErr, &sdkErr) && sdkErr.Code == "DOCKER_NOT_MODIFIED" {
			already = true
		} else {
			return nil, callErr
		}
	}
	data, err := json.Marshal(lifecycleResult{
		ID: p.ID, Action: action, AlreadyInState: already,
	})
	if err != nil {
		return nil, sdk.NewError("ENCODE_FAILED", err.Error(), err)
	}
	return &sdk.ExecuteResponse{Data: data}, nil
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// decodeParams 与 plugins/ssh 保持一致：允许 params 为空 / 空 JSON。
func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed", err)
	}
	return nil
}

// isSDKError 断言 err 是 *sdk.Error（含用 errors.As 兜底）。
func isSDKError(err error, target **sdk.Error) bool {
	if e, ok := err.(*sdk.Error); ok {
		*target = e
		return true
	}
	return false
}
