// Package conformance 提供插件作者可以直接调用的 SDK 一致性测试套件
// 与配套的 fake Core / fake Stream 工具。
//
// 典型用法（放在插件的 _test.go 里）：
//
//	func TestConformance(t *testing.T) {
//		conformance.Run(t, conformance.Suite{
//			Plugin: newMyPlugin(),
//		})
//	}
//
// 该包不引入 gRPC / hashicorp/go-plugin 依赖，纯进程内驱动 Plugin 抽象。
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/mow/mow/sdk"
)

// FakeStream 是 sdk.Stream 的 in-memory 实现，供插件流式 Command 的单测使用。
//
// 生命周期：
//  1. NewFakeStream(ctx, opts) 构造实例
//  2. 调用方按需 Push(...) 客户端入站消息（Stdin/Signal）
//  3. 把 FakeStream 传给 handler.ExecuteStream(ctx, stream)
//  4. 通过 Stdout()/Stderr()/Events()/FinalData()/ExitCode() 读取插件输出
//
// FakeStream 对并发安全：多 goroutine 可同时 Push / Stdout / Finish。
type FakeStream struct {
	ctx       context.Context
	cancelFn  context.CancelFunc
	auditID   string
	caller    sdk.Caller
	confirmed bool
	rawParams json.RawMessage
	conn      *sdk.Connection
	incoming  chan sdk.Incoming
	closeOnce sync.Once

	mu       sync.Mutex
	stdout   [][]byte
	stderr   [][]byte
	events   []json.RawMessage
	final    json.RawMessage
	exitCode int
	finished bool
	finErr   error
}

// FakeStreamOptions 配置 FakeStream 的初始状态。
// 所有字段可选：零值即最简一次流式调用。
type FakeStreamOptions struct {
	AuditID    string
	Caller     sdk.Caller
	Confirmed  bool
	Params     any // 会被 json.Marshal 后存入 RawParams
	RawParams  json.RawMessage
	Connection *sdk.Connection
	// InboxSize 是入站队列缓冲；默认 16。
	InboxSize int
}

// NewFakeStream 构造一个可控的 fake Stream。
// 返回的 stream 已经绑定 ctx；调用 Cancel() 相当于用户在 Core 侧取消。
func NewFakeStream(ctx context.Context, opts FakeStreamOptions) *FakeStream {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.InboxSize <= 0 {
		opts.InboxSize = 16
	}
	raw := opts.RawParams
	if raw == nil && opts.Params != nil {
		b, err := json.Marshal(opts.Params)
		if err == nil {
			raw = b
		}
	}
	c, cancel := context.WithCancel(ctx)
	return &FakeStream{
		ctx:       c,
		cancelFn:  cancel,
		auditID:   opts.AuditID,
		caller:    opts.Caller,
		confirmed: opts.Confirmed,
		rawParams: raw,
		conn:      opts.Connection,
		incoming:  make(chan sdk.Incoming, opts.InboxSize),
	}
}

// --- sdk.Stream 实现 --------------------------------------------------------

func (s *FakeStream) Context() context.Context   { return s.ctx }
func (s *FakeStream) AuditID() string            { return s.auditID }
func (s *FakeStream) Caller() sdk.Caller         { return s.caller }
func (s *FakeStream) Confirmed() bool            { return s.confirmed }
func (s *FakeStream) RawParams() json.RawMessage { return s.rawParams }
func (s *FakeStream) Connection() *sdk.Connection {
	return s.conn
}

// Params 把首帧参数解码到 dst；无参数时返回 nil。
func (s *FakeStream) Params(dst any) error {
	if len(s.rawParams) == 0 {
		return nil
	}
	return json.Unmarshal(s.rawParams, dst)
}

// Recv 返回入站通道。调用方通过 Push / CloseInbox 驱动。
func (s *FakeStream) Recv() <-chan sdk.Incoming { return s.incoming }

// Stdout 记录一次插件写出的标准输出。
func (s *FakeStream) Stdout(data []byte) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	buf := append([]byte(nil), data...)
	s.mu.Lock()
	s.stdout = append(s.stdout, buf)
	s.mu.Unlock()
	return nil
}

// Stderr 记录一次插件写出的标准错误。
func (s *FakeStream) Stderr(data []byte) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	buf := append([]byte(nil), data...)
	s.mu.Lock()
	s.stderr = append(s.stderr, buf)
	s.mu.Unlock()
	return nil
}

// Event 序列化 v 并追加到事件列表。
func (s *FakeStream) Event(v any) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.events = append(s.events, b)
	s.mu.Unlock()
	return nil
}

// Finish 记录终态。多次调用只会记录第一次。
func (s *FakeStream) Finish(finalData any, exitCode int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return errors.New("conformance: stream already finished")
	}
	s.finished = true
	s.exitCode = exitCode
	if finalData != nil {
		b, err := json.Marshal(finalData)
		if err != nil {
			s.finErr = err
			return err
		}
		s.final = b
	}
	return nil
}

// --- 测试辅助 ---------------------------------------------------------------

// Push 追加一条入站消息（例如 &sdk.Stdin{Data: ...}）。
// 若 inbox 已满则阻塞直至 ctx 完成。
func (s *FakeStream) Push(m sdk.Incoming) {
	select {
	case <-s.ctx.Done():
	case s.incoming <- m:
	}
}

// PushStdin 快捷方法：等价于 Push(&sdk.Stdin{Data: b, At: now}).
func (s *FakeStream) PushStdin(b []byte) {
	s.Push(&sdk.Stdin{Data: append([]byte(nil), b...), At: time.Now()})
}

// PushSignal 快捷方法：等价于 Push(&sdk.Signal{Type: t, At: now}).
func (s *FakeStream) PushSignal(t sdk.SignalType) {
	s.Push(&sdk.Signal{Type: t, At: time.Now()})
}

// CloseInbox 关闭入站通道，模拟客户端断开。
// 幂等：多次调用只关闭一次。
func (s *FakeStream) CloseInbox() {
	s.closeOnce.Do(func() { close(s.incoming) })
}

// Cancel 取消 stream 的 Context，等价于 Core 侧取消调用。
func (s *FakeStream) Cancel() { s.cancelFn() }

// Stdout 返回累计写出的所有 stdout 块（浅拷贝，调用方不得原地修改）。
func (s *FakeStream) StdoutChunks() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.stdout))
	copy(out, s.stdout)
	return out
}

// StderrChunks 返回累计写出的所有 stderr 块。
func (s *FakeStream) StderrChunks() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.stderr))
	copy(out, s.stderr)
	return out
}

// Events 返回累计发送的事件（每条为原始 JSON）。
func (s *FakeStream) Events() []json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]json.RawMessage, len(s.events))
	copy(out, s.events)
	return out
}

// FinalData 返回 Finish 时提交的 finalData 原始 JSON（未调用时为 nil）。
func (s *FakeStream) FinalData() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.final
}

// ExitCode 返回 Finish 时提交的 exit code。
func (s *FakeStream) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// Finished 报告 Finish 是否已被调用过。
func (s *FakeStream) Finished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}
