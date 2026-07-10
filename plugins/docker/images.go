// images.go —— v0.3 第三阶段：docker.pull / docker.push。
//
// 语义要点：
//   - pull：POST /images/create?fromImage=<name>&tag=<tag>
//   - push：POST /images/<name>/push?tag=<tag>；必须携带 X-Registry-Auth
//   - 两者响应体都是 chunked JSON lines，格式：
//         {"status":"...","progressDetail":{...},"id":"..."}
//         {"errorDetail":{"message":"..."},"error":"..."}
//     单行 error → 整个操作失败
//
// 输出：
//   - 每行 progress 通过 s.Event 广播
//   - 结束 s.Finish(summary, 0)；出错 return sdk.Error

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// 共享结构
// -----------------------------------------------------------------------------

// registryAuth 与 Docker Engine 的 AuthConfig 对齐（局部）。
// 会被 base64(json) 编码后放到 X-Registry-Auth 头。
type registryAuth struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	Email         string `json:"email,omitempty"`
	Serveraddress string `json:"serveraddress,omitempty"`
	// IdentityToken：短期令牌（如 ECR、GCR），与 username/password 二选一。
	IdentityToken string `json:"identitytoken,omitempty"`
}

// encodeAuth 生成 X-Registry-Auth 头值。
// Engine 要求 base64 后原封不动即可（url-safe / 常规 base64 都可接受）。
func encodeAuth(a *registryAuth) (string, error) {
	if a == nil || (a.Username == "" && a.IdentityToken == "") {
		// Engine 对匿名 push 也要求头字段存在；给一个空 JSON 就好。
		return base64.URLEncoding.EncodeToString([]byte("{}")), nil
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(raw), nil
}

// progressLine 是 pull / push 每行 JSON 的字段（局部）。
type progressLine struct {
	Status         string          `json:"status,omitempty"`
	ID             string          `json:"id,omitempty"`
	ProgressDetail json.RawMessage `json:"progressDetail,omitempty"`
	Progress       string          `json:"progress,omitempty"`

	// 错误：任一非空表示整个操作失败。
	Error       string             `json:"error,omitempty"`
	ErrorDetail *progressErrDetail `json:"errorDetail,omitempty"`
}

type progressErrDetail struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// pumpProgress 逐行读取 chunked JSON body，非空行经 s.Event 转发。
//
// 单行 error 命中 → 返回 sdk.Error，不发 Finish（由外层 return）。
// io.EOF → 正常结束。
func pumpProgress(ctx context.Context, r io.Reader, s sdk.Stream) error {
	// Engine 有时会把多个 JSON 塞进一个 line，用 json.Decoder 更稳。
	dec := json.NewDecoder(r)
	for {
		if ctx.Err() != nil {
			// 上层 ctx 取消：转发底层 err 即可，Engine 会随连接断开自然收尾。
			return mapReadErr(ctx, ctx.Err())
		}
		var line progressLine
		if err := dec.Decode(&line); err != nil {
			if err == io.EOF {
				return nil
			}
			return mapReadErr(ctx, err)
		}
		if line.Error != "" || (line.ErrorDetail != nil && line.ErrorDetail.Message != "") {
			msg := line.Error
			if msg == "" {
				msg = line.ErrorDetail.Message
			}
			return sdk.NewError(classifyRegistryError(msg), msg, nil)
		}
		// 已知空行 —— 一般是保留字段全空的心跳。
		if line.Status == "" && line.ID == "" && line.Progress == "" {
			continue
		}
		if err := s.Event(line); err != nil {
			return err
		}
	}
}

// classifyRegistryError 从错误字符串猜错误码；找不到 fallback 到 DOCKER_REGISTRY_ERROR。
func classifyRegistryError(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "unauthorized"), strings.Contains(m, "authentication required"):
		return "DOCKER_UNAUTHORIZED"
	case strings.Contains(m, "not found"), strings.Contains(m, "manifest unknown"):
		return "DOCKER_NOT_FOUND"
	case strings.Contains(m, "denied"):
		return "DOCKER_FORBIDDEN"
	default:
		return "DOCKER_REGISTRY_ERROR"
	}
}

// -----------------------------------------------------------------------------
// docker.pull
// -----------------------------------------------------------------------------

type pullParams struct {
	// FromImage：镜像仓库名（例："nginx"、"registry.example.com/team/app"）。必填。
	FromImage string `json:"from_image"`
	// Tag：标签或 digest；空 → 走 Engine 默认（一般是 latest）。
	Tag string `json:"tag,omitempty"`
	// Platform：例 "linux/amd64"，用于多架构镜像。
	Platform string `json:"platform,omitempty"`
	// Auth：可选私有仓库凭据。
	Auth *registryAuth `json:"auth,omitempty"`
}

type pullResult struct {
	Image    string `json:"image"`
	Tag      string `json:"tag,omitempty"`
	Platform string `json:"platform,omitempty"`
}

type pullCmd struct{}

func (c *pullCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "pull",
		Description:    "pull an image from a registry (streams progress events)",
		Permission:     sdk.PermExecute,
		ConnectionType: "docker",
		Streaming:      true,
	}
}

func (c *pullCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return nil, sdk.ErrNotSupported
}

func (c *pullCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	conn := s.Connection()
	if conn == nil {
		return sdk.ErrConnectionRequired
	}
	var p pullParams
	if err := s.Params(&p); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed", err)
	}
	if p.FromImage == "" {
		return sdk.NewError("PARAM_INVALID", "from_image is required", nil)
	}

	dt, err := resolveTarget(conn)
	if err != nil {
		return sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	cli, err := newEngineClient(dt)
	if err != nil {
		return sdk.NewError("DOCKER_CLIENT_INVALID", err.Error(), err)
	}
	defer cli.closeIdle()

	q := url.Values{}
	q.Set("fromImage", p.FromImage)
	if p.Tag != "" {
		q.Set("tag", p.Tag)
	}
	if p.Platform != "" {
		q.Set("platform", p.Platform)
	}

	headers := map[string]string{}
	if p.Auth != nil {
		enc, err := encodeAuth(p.Auth)
		if err != nil {
			return sdk.NewError("PARAM_INVALID", "encode auth: "+err.Error(), err)
		}
		headers["X-Registry-Auth"] = enc
	}

	// 监听 stream 上的取消信号
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchSignals(streamCtx, s, cancel)

	resp, err := cli.doWithBody(streamCtx, http.MethodPost, "/images/create", q,
		nil, "application/json", headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := pumpProgress(streamCtx, resp.Body, s); err != nil {
		return err
	}
	return s.Finish(pullResult{Image: p.FromImage, Tag: p.Tag, Platform: p.Platform}, 0)
}

// -----------------------------------------------------------------------------
// docker.push
// -----------------------------------------------------------------------------

type pushParams struct {
	// Image：本地镜像名（例："registry.example.com/team/app"）。必填。
	Image string `json:"image"`
	Tag   string `json:"tag,omitempty"`

	// Auth：Engine 强制要求 X-Registry-Auth 头存在；未指定时插件填充 "{}"。
	// 但真正的 push 通常需要 username / password 或 identitytoken。
	Auth *registryAuth `json:"auth,omitempty"`
}

type pushResult struct {
	Image string `json:"image"`
	Tag   string `json:"tag,omitempty"`
}

type pushCmd struct{}

func (c *pushCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:             "push",
		Description:    "push a local image to a registry (streams progress events)",
		Permission:     sdk.PermExecute,
		ConnectionType: "docker",
		Streaming:      true,
	}
}

func (c *pushCmd) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return nil, sdk.ErrNotSupported
}

func (c *pushCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	conn := s.Connection()
	if conn == nil {
		return sdk.ErrConnectionRequired
	}
	var p pushParams
	if err := s.Params(&p); err != nil {
		return sdk.NewError("PARAM_INVALID", "decode params failed", err)
	}
	if p.Image == "" {
		return sdk.NewError("PARAM_INVALID", "image is required", nil)
	}
	dt, err := resolveTarget(conn)
	if err != nil {
		return sdk.NewError("CONNECTION_INVALID", err.Error(), err)
	}
	cli, err := newEngineClient(dt)
	if err != nil {
		return sdk.NewError("DOCKER_CLIENT_INVALID", err.Error(), err)
	}
	defer cli.closeIdle()

	q := url.Values{}
	if p.Tag != "" {
		q.Set("tag", p.Tag)
	}
	enc, err := encodeAuth(p.Auth)
	if err != nil {
		return sdk.NewError("PARAM_INVALID", "encode auth: "+err.Error(), err)
	}
	headers := map[string]string{"X-Registry-Auth": enc}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchSignals(streamCtx, s, cancel)

	resp, err := cli.doWithBody(streamCtx, http.MethodPost,
		"/images/"+url.PathEscape(p.Image)+"/push", q,
		nil, "application/json", headers)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := pumpProgress(streamCtx, resp.Body, s); err != nil {
		return err
	}
	return s.Finish(pushResult{Image: p.Image, Tag: p.Tag}, 0)
}
