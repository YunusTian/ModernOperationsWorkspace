// desktopDockerExecStream 是 sdk.Stream 的桌面端实现，对齐 docker.exec 双向流。
//
// 与 desktopShellStream 的差异：
//   - 事件前缀 "docker:exec:<sid>:*"
//   - 允许 attach_stdin=false 时也接收 pushStdin（后端插件会丢弃）
//
// 事件契约：
//
//	docker:exec:<sid>:stdout   base64(bytes)
//	docker:exec:<sid>:stderr   base64(bytes)
//	docker:exec:<sid>:event    raw json
//	docker:exec:<sid>:exit     { exit_code, error? }
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/mow/mow/sdk"
)

type desktopDockerExecStream struct {
	ctx       context.Context
	wailsCtx  context.Context
	sessionID string
	auditID   string
	params    json.RawMessage
	incoming  chan sdk.Incoming
	finished  atomic.Bool
	exitCd    atomic.Int32

	connRef atomic.Value // *sdk.Connection

	closeOnce sync.Once
}

func newDesktopDockerExecStream(ctx, wailsCtx context.Context, sessionID string) *desktopDockerExecStream {
	return &desktopDockerExecStream{
		ctx:       ctx,
		wailsCtx:  wailsCtx,
		sessionID: sessionID,
		incoming:  make(chan sdk.Incoming, 64),
	}
}

func (s *desktopDockerExecStream) setParams(p json.RawMessage) { s.params = p }
func (s *desktopDockerExecStream) exitCode() int               { return int(s.exitCd.Load()) }

func (s *desktopDockerExecStream) SetAuditID(id string)                 { s.auditID = id }
func (s *desktopDockerExecStream) SetConnection(c *sdk.Connection)      { s.connRef.Store(c) }
func (s *desktopDockerExecStream) Context() context.Context             { return s.ctx }
func (s *desktopDockerExecStream) AuditID() string {
	if s.auditID != "" {
		return s.auditID
	}
	return s.sessionID
}
func (s *desktopDockerExecStream) Caller() sdk.Caller     { return sdk.Caller{Type: sdk.CallerDesktop} }
func (s *desktopDockerExecStream) Confirmed() bool        { return true }
func (s *desktopDockerExecStream) RawParams() json.RawMessage { return s.params }
func (s *desktopDockerExecStream) Params(dst any) error {
	if len(s.params) == 0 {
		return nil
	}
	return json.Unmarshal(s.params, dst)
}
func (s *desktopDockerExecStream) Connection() *sdk.Connection {
	v := s.connRef.Load()
	if v == nil {
		return nil
	}
	return v.(*sdk.Connection)
}
func (s *desktopDockerExecStream) Recv() <-chan sdk.Incoming { return s.incoming }

func (s *desktopDockerExecStream) Stdout(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	wailsruntime.EventsEmit(s.wailsCtx, "docker:exec:"+s.sessionID+":stdout",
		base64.StdEncoding.EncodeToString(data))
	return nil
}
func (s *desktopDockerExecStream) Stderr(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	wailsruntime.EventsEmit(s.wailsCtx, "docker:exec:"+s.sessionID+":stderr",
		base64.StdEncoding.EncodeToString(data))
	return nil
}
func (s *desktopDockerExecStream) Event(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wailsruntime.EventsEmit(s.wailsCtx, "docker:exec:"+s.sessionID+":event", json.RawMessage(b))
	return nil
}
func (s *desktopDockerExecStream) Finish(_ any, exitCode int) error {
	if s.finished.Swap(true) {
		return nil
	}
	s.exitCd.Store(int32(exitCode))
	return nil
}

func (s *desktopDockerExecStream) pushStdin(b []byte) {
	select {
	case s.incoming <- &sdk.Stdin{Data: b, At: time.Now()}:
	case <-s.ctx.Done():
	}
}
func (s *desktopDockerExecStream) pushWinch(rows, cols int) {
	payload, _ := json.Marshal(map[string]int{"rows": rows, "cols": cols})
	select {
	case s.incoming <- &sdk.Signal{Type: sdk.SignalWinch, Payload: payload, At: time.Now()}:
	case <-s.ctx.Done():
	}
}
func (s *desktopDockerExecStream) close() {
	s.closeOnce.Do(func() { close(s.incoming) })
}
