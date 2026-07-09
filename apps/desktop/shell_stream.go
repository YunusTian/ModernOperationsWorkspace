// desktopShellStream 是 sdk.Stream 的桌面端实现。
//
// 它把 ssh.shell 插件与前端 xterm.js 之间的双向流"就地短接"：
//   - 插件端 stdout/stderr → EventsEmit 到 "shell:<sid>:stdout" / ":stderr"
//   - 前端 stdin/resize    → pushStdin / pushWinch → incoming 通道
//
// 前端约定：
//   - "shell:<sid>:stdout"  payload = base64(bytes)
//   - "shell:<sid>:stderr"  payload = base64(bytes)
//   - "shell:<sid>:exit"    payload = { exit_code, error? }
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

type desktopShellStream struct {
	ctx       context.Context // Command 的 ctx（cancel → 结束流）
	wailsCtx  context.Context // wails runtime 上下文，供 EventsEmit
	sessionID string
	params    json.RawMessage
	incoming  chan sdk.Incoming
	finished  atomic.Bool
	exitCd    atomic.Int32

	connRef atomic.Value // *sdk.Connection

	closeOnce sync.Once
}

func newDesktopShellStream(ctx, wailsCtx context.Context, sessionID string) *desktopShellStream {
	return &desktopShellStream{
		ctx:       ctx,
		wailsCtx:  wailsCtx,
		sessionID: sessionID,
		incoming:  make(chan sdk.Incoming, 32),
	}
}

func (s *desktopShellStream) setParams(p json.RawMessage) { s.params = p }
func (s *desktopShellStream) exitCode() int               { return int(s.exitCd.Load()) }

// SetConnection 由 App 在启动流之前挂上 core/connection 解出的连接。
// 见 App.ShellOpen —— 因为 core/command.RunStream 只把连接写进 Invocation.Request，
// 不会自动注入 stream，我们必须在这里显式绑定。
func (s *desktopShellStream) SetConnection(c *sdk.Connection) { s.connRef.Store(c) }

// -----------------------------------------------------------------------------
// sdk.Stream 接口
// -----------------------------------------------------------------------------

func (s *desktopShellStream) Context() context.Context { return s.ctx }
func (s *desktopShellStream) AuditID() string          { return s.sessionID }
func (s *desktopShellStream) Caller() sdk.Caller       { return sdk.Caller{Type: sdk.CallerDesktop} }
func (s *desktopShellStream) Confirmed() bool          { return true }
func (s *desktopShellStream) RawParams() json.RawMessage {
	return s.params
}
func (s *desktopShellStream) Params(dst any) error {
	if len(s.params) == 0 {
		return nil
	}
	return json.Unmarshal(s.params, dst)
}

func (s *desktopShellStream) Connection() *sdk.Connection {
	v := s.connRef.Load()
	if v == nil {
		return nil
	}
	return v.(*sdk.Connection)
}

func (s *desktopShellStream) Recv() <-chan sdk.Incoming { return s.incoming }

func (s *desktopShellStream) Stdout(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	wailsruntime.EventsEmit(s.wailsCtx, "shell:"+s.sessionID+":stdout",
		base64.StdEncoding.EncodeToString(data))
	return nil
}

func (s *desktopShellStream) Stderr(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	wailsruntime.EventsEmit(s.wailsCtx, "shell:"+s.sessionID+":stderr",
		base64.StdEncoding.EncodeToString(data))
	return nil
}

func (s *desktopShellStream) Event(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	wailsruntime.EventsEmit(s.wailsCtx, "shell:"+s.sessionID+":event", json.RawMessage(b))
	return nil
}

func (s *desktopShellStream) Finish(finalData any, exitCode int) error {
	if s.finished.Swap(true) {
		return nil
	}
	s.exitCd.Store(int32(exitCode))
	return nil
}

// -----------------------------------------------------------------------------
// pushStdin / pushWinch —— 前端通过 App 方法调用
// -----------------------------------------------------------------------------

func (s *desktopShellStream) pushStdin(b []byte) {
	select {
	case s.incoming <- &sdk.Stdin{Data: b, At: time.Now()}:
	case <-s.ctx.Done():
	}
}

func (s *desktopShellStream) pushWinch(rows, cols int) {
	payload, _ := json.Marshal(map[string]int{"rows": rows, "cols": cols})
	select {
	case s.incoming <- &sdk.Signal{Type: sdk.SignalWinch, Payload: payload, At: time.Now()}:
	case <-s.ctx.Done():
	}
}

// close 结束 incoming 通道，让插件端 Recv() 走 EOF 分支。
func (s *desktopShellStream) close() {
	s.closeOnce.Do(func() { close(s.incoming) })
}
