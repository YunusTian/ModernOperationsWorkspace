// docker.go 桌面客户端的 Docker Dashboard 能力（v0.3 第二阶段）。
//
// 主路径（严格顺序）：
//
//	容器列表 (DockerList)
//	   │
//	   ├── DockerInspect 抽屉  ── 只读
//	   │
//	   ├── DockerLogsOpen (流式，事件推送 stdout/stderr/exit)
//	   │
//	   └── 生命周期动作 (DockerStart / DockerStop / DockerRestart)
//	       ↑ 前端弹窗后置 confirmed=true，才会真正下发
//
// 不做的事（第三阶段承接）：镜像 / 卷 / 网络 / Compose / rm / exec / push / pull。
//
// 前端事件（Wails EventsEmit）：
//
//	docker:logs:<session>:stdout   base64(bytes)
//	docker:logs:<session>:stderr   base64(bytes)
//	docker:logs:<session>:exit     { exit_code, error? }

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// 类型：DockerList 结果（与 plugins/docker 的 listResult 字段对齐）
// -----------------------------------------------------------------------------

// DockerPort 是端口映射的前端投影。
type DockerPort struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"private_port"`
	PublicPort  int    `json:"public_port,omitempty"`
	Type        string `json:"type,omitempty"`
}

// DockerContainerVM 是列表页每一行。
type DockerContainerVM struct {
	ID      string            `json:"id"`
	Names   []string          `json:"names"`
	Image   string            `json:"image"`
	ImageID string            `json:"image_id,omitempty"`
	Command string            `json:"command,omitempty"`
	Created int64             `json:"created,omitempty"`
	State   string            `json:"state"`
	Status  string            `json:"status,omitempty"`
	Ports   []DockerPort      `json:"ports,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// DockerListResult 与插件 listResult 结构对齐。
type DockerListResult struct {
	Containers []DockerContainerVM `json:"containers"`
	AuditID    string              `json:"audit_id"`
}

// -----------------------------------------------------------------------------
// DockerList
// -----------------------------------------------------------------------------

// DockerListInput 是 DockerList 的入参。
type DockerListInput struct {
	// All 为 true 时列出所有容器（包含 exited）。
	All bool `json:"all,omitempty"`
	// Limit 限制返回条数。
	Limit int `json:"limit,omitempty"`
	// Labels 是可选的 label 过滤。
	Labels map[string]string `json:"labels,omitempty"`
}

// DockerList 走 Command Engine 调 docker.list。
func (a *App) DockerList(targetID string, in DockerListInput) (*DockerListResult, error) {
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 30*time.Second)
	defer cancel()
	if err := a.ensurePlugin(ctx, "docker"); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(in)
	resp, err := a.engine.Run(ctx, command.Request{
		PluginID:  "docker",
		CommandID: "list",
		TargetID:  targetID,
		Params:    params,
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	})
	if err != nil {
		return nil, err
	}
	var raw struct {
		Containers []DockerContainerVM `json:"containers"`
	}
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return nil, fmt.Errorf("decode list: %w", err)
	}
	return &DockerListResult{Containers: raw.Containers, AuditID: resp.AuditID}, nil
}

// -----------------------------------------------------------------------------
// DockerInspect
// -----------------------------------------------------------------------------

// DockerInspectResult 内嵌 Engine 原始 JSON（前端可自由渲染字段）+ AuditID。
type DockerInspectResult struct {
	AuditID string          `json:"audit_id"`
	Raw     json.RawMessage `json:"raw"`
}

// DockerInspect 走 Command Engine 调 docker.inspect。
func (a *App) DockerInspect(targetID, containerID string) (*DockerInspectResult, error) {
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 15*time.Second)
	defer cancel()
	if err := a.ensurePlugin(ctx, "docker"); err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{"id": containerID})
	resp, err := a.engine.Run(ctx, command.Request{
		PluginID:  "docker",
		CommandID: "inspect",
		TargetID:  targetID,
		Params:    params,
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	})
	if err != nil {
		return nil, err
	}
	return &DockerInspectResult{AuditID: resp.AuditID, Raw: resp.Data}, nil
}

// -----------------------------------------------------------------------------
// DockerLifecycle：start / stop / restart
// -----------------------------------------------------------------------------

// DockerLifecycleInput 是 start/stop/restart 的通用入参。
// Confirmed 语义：前端弹窗后由用户按下"确认"传 true；后端在此显式校验以避免
// 由于 Command.AllowConfirmer 让任何调用都跳过确认。
type DockerLifecycleInput struct {
	Action     string `json:"action"` // start / stop / restart
	Container  string `json:"container"`
	TimeoutSec int    `json:"timeout_sec,omitempty"` // stop / restart 生效
	Confirmed  bool   `json:"confirmed"`
}

// DockerLifecycleResult 与插件返回对齐。
type DockerLifecycleResult struct {
	AuditID        string `json:"audit_id"`
	ID             string `json:"id"`
	Action         string `json:"action"`
	AlreadyInState bool   `json:"already_in_state"`
}

// DockerLifecycle 执行 start / stop / restart。
//
// 前端必须先给 Confirmed=true；否则直接拒绝——这是应用层的保护，
// 与 Command Engine 的 Dangerous 二次确认无关（start/stop/restart 权限为 Execute）。
func (a *App) DockerLifecycle(targetID string, in DockerLifecycleInput) (*DockerLifecycleResult, error) {
	if !in.Confirmed {
		return nil, fmt.Errorf("dashboard: refuse to run docker.%s without user confirmation", in.Action)
	}
	switch in.Action {
	case "start", "stop", "restart":
	default:
		return nil, fmt.Errorf("dashboard: unsupported docker action %q", in.Action)
	}
	if in.Container == "" {
		return nil, fmt.Errorf("dashboard: container is required")
	}
	ctx, cancel := context.WithTimeout(a.wailsCtx(), 60*time.Second)
	defer cancel()
	if err := a.ensurePlugin(ctx, "docker"); err != nil {
		return nil, err
	}
	body := map[string]any{"id": in.Container}
	if (in.Action == "stop" || in.Action == "restart") && in.TimeoutSec > 0 {
		body["timeout_sec"] = in.TimeoutSec
	}
	params, _ := json.Marshal(body)
	resp, err := a.engine.Run(ctx, command.Request{
		PluginID:  "docker",
		CommandID: in.Action,
		TargetID:  targetID,
		Params:    params,
		Caller:    sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
	})
	if err != nil {
		return nil, err
	}
	out := DockerLifecycleResult{AuditID: resp.AuditID}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		return nil, fmt.Errorf("decode lifecycle: %w", err)
	}
	return &out, nil
}

// -----------------------------------------------------------------------------
// DockerLogs：流式（打开 / 关闭 + wails 事件）
// -----------------------------------------------------------------------------
//
// 语义：
//   DockerLogsOpen(targetID, in)   → sessionID
//   DockerLogsClose(sessionID)     → 主动关闭
//   事件：docker:logs:<sid>:stdout / :stderr / :exit
//
// 与 SSH shell 会话相似但只走单向输出（无 stdin / 无 winch）。

// DockerLogsInput 是 DockerLogsOpen 的入参（与 plugins/docker.logsParams 对齐）。
type DockerLogsInput struct {
	Container  string `json:"container"`
	Follow     bool   `json:"follow,omitempty"`
	Tail       string `json:"tail,omitempty"`
	Stdout     *bool  `json:"stdout,omitempty"`
	Stderr     *bool  `json:"stderr,omitempty"`
	Timestamps bool   `json:"timestamps,omitempty"`
	Since      int64  `json:"since,omitempty"`
	Until      int64  `json:"until,omitempty"`
	Tty        bool   `json:"tty,omitempty"`
}

// dockerLogsSession 保存一个正在运行的日志流。
type dockerLogsSession struct {
	id     string
	cancel context.CancelFunc
	stream *dockerLogsStream
}

var dockerLogsSeq atomic.Int64

// DockerLogsOpen 打开一次流式日志。前端应在收到 `:exit` 事件后停止渲染。
func (a *App) DockerLogsOpen(targetID string, in DockerLogsInput) (string, error) {
	if in.Container == "" {
		return "", fmt.Errorf("dashboard: container is required")
	}
	sess := fmt.Sprintf("dl-%d", dockerLogsSeq.Add(1))
	rootCtx := a.wailsCtx()

	ctx, cancel := context.WithCancel(rootCtx)
	if err := a.ensurePlugin(ctx, "docker"); err != nil {
		cancel()
		return "", err
	}

	// 提前解析连接，见 ShellOpen 的注释：core/command.RunStream 会在 Middleware
	// 把 Connection 写进 Request；但流式 Command 拿的是 stream.Connection()。
	conn, err := a.connMgr.Open(ctx, targetID)
	if err != nil {
		cancel()
		return "", fmt.Errorf("open target %q: %w", targetID, err)
	}

	// 组装 params：把 Container 映射为 "id" 与插件契约对齐。
	body := map[string]any{
		"id":         in.Container,
		"follow":     in.Follow,
		"timestamps": in.Timestamps,
		"tty":        in.Tty,
	}
	if in.Tail != "" {
		body["tail"] = in.Tail
	}
	if in.Stdout != nil {
		body["stdout"] = *in.Stdout
	}
	if in.Stderr != nil {
		body["stderr"] = *in.Stderr
	}
	if in.Since > 0 {
		body["since"] = in.Since
	}
	if in.Until > 0 {
		body["until"] = in.Until
	}
	params, _ := json.Marshal(body)

	stream := newDockerLogsStream(ctx, a.wailsCtx(), sess)
	stream.setParams(params)
	stream.SetConnection(conn)

	sessObj := &dockerLogsSession{id: sess, cancel: cancel, stream: stream}
	a.dockerLogs.Store(sess, sessObj)

	go func() {
		defer a.dockerLogs.Delete(sess)
		defer stream.close()

		req := command.Request{
			PluginID:   "docker",
			CommandID:  "logs",
			TargetID:   targetID,
			Connection: conn,
			Params:     params,
			Caller:     sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
		}
		err := a.engine.RunStream(ctx, req, stream)
		stream.emitExit(err)
	}()

	return sess, nil
}

// DockerLogsClose 主动关闭一个日志会话。若 session 不存在，返回 nil（幂等）。
func (a *App) DockerLogsClose(sessionID string) error {
	v, ok := a.dockerLogs.LoadAndDelete(sessionID)
	if !ok {
		return nil
	}
	v.(*dockerLogsSession).cancel()
	return nil
}
