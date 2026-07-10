// docker_logs_stream.go —— docker.logs 的 sdk.Stream 桌面端实现。
//
// 与 shell_stream.go 结构相似，但只走单向输出：
//   - 插件端 Stdout / Stderr → EventsEmit 到 "docker:logs:<sid>:stdout|:stderr"
//   - 前端无 stdin；只通过 DockerLogsClose 触发 cancel
//
// 事件契约：
//   "docker:logs:<sid>:stdout"  payload = base64(bytes)
//   "docker:logs:<sid>:stderr"  payload = base64(bytes)
//   "docker:logs:<sid>:exit"    payload = { error?, audit_id }
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"sync/atomic"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/mow/mow/sdk"
)

type dockerLogsStream struct {
	ctx       context.Context
	wailsCtx  context.Context
	sessionID string
	auditID   string
	params    json.RawMessage
	incoming  chan sdk.Incoming
	finished  atomic.Bool

	connRef   atomic.Value // *sdk.Connection
	closeOnce sync.Once
}

func newDockerLogsStream(ctx, wailsCtx context.Context, sessionID string) *dockerLogsStream {
	return &dockerLogsStream{
		ctx:       ctx,
		wailsCtx:  wailsCtx,
		sessionID: sessionID,
		// docker.logs 无 stdin；incoming 仅用于承载潜在的 Cancel 信号（当前未主动使用）。
		incoming: make(chan sdk.Incoming, 4),
	}
}

func (s *dockerLogsStream) setParams(p json.RawMessage) { s.params = p }

// SetAuditID 由 Engine.RunStream 在调用 ExecuteStream 前注入。
func (s *dockerLogsStream) SetAuditID(id string) { s.auditID = id }

// SetConnection 由 App 显式挂载。
func (s *dockerLogsStream) SetConnection(c *sdk.Connection) { s.connRef.Store(c) }

// -----------------------------------------------------------------------------
// sdk.Stream 接口
// -----------------------------------------------------------------------------

func (s *dockerLogsStream) Context() context.Context { return s.ctx }
func (s *dockerLogsStream) AuditID() string {
	if s.auditID != "" {
		return s.auditID
	}
	return s.sessionID
}
func (s *dockerLogsStream) Caller() sdk.Caller           { return sdk.Caller{Type: sdk.CallerDesktop} }
func (s *dockerLogsStream) Confirmed() bool              { return true }
func (s *dockerLogsStream) RawParams() json.RawMessage   { return s.params }
func (s *dockerLogsStream) Params(dst any) error {
	if len(s.params) == 0 {
		return nil
	}
	return json.Unmarshal(s.params, dst)
}
func (s *dockerLogsStream) Connection() *sdk.Connection {
	v := s.connRef.Load()
	if v == nil {
		return nil
	}
	return v.(*sdk.Connection)
}
func (s *dockerLogsStream) Recv() <-chan sdk.Incoming { return s.incoming }

func (s *dockerLogsStream) Stdout(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	wailsruntime.EventsEmit(s.wailsCtx, "docker:logs:"+s.sessionID+":stdout",
		base64.StdEncoding.EncodeToString(data))
	return nil
}

func (s *dockerLogsStream) Stderr(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	wailsruntime.EventsEmit(s.wailsCtx, "docker:logs:"+s.sessionID+":stderr",
		base64.StdEncoding.EncodeToString(data))
	return nil
}

func (s *dockerLogsStream) Event(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wailsruntime.EventsEmit(s.wailsCtx, "docker:logs:"+s.sessionID+":event", json.RawMessage(b))
	return nil
}

func (s *dockerLogsStream) Finish(_ any, _ int) error {
	s.finished.Store(true)
	return nil
}

// -----------------------------------------------------------------------------
// 辅助方法
// -----------------------------------------------------------------------------

// emitExit 由 goroutine 在 RunStream 返回后调用；把最终 error / auditID 推给前端。
func (s *dockerLogsStream) emitExit(runErr error) {
	payload := map[string]any{"audit_id": s.auditID}
	if runErr != nil {
		payload["error"] = runErr.Error()
	}
	wailsruntime.EventsEmit(s.wailsCtx, "docker:logs:"+s.sessionID+":exit", payload)
}

// close 关闭 incoming 通道；日志场景下不常触发（无 stdin），保留以对齐 sdk.Stream 语义。
func (s *dockerLogsStream) close() {
	s.closeOnce.Do(func() { close(s.incoming) })
}
