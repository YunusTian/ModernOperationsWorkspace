package sdk

import (
	"context"
	"encoding/json"
	"time"
)

// -----------------------------------------------------------------------------
// Stream（流式 Command 的抽象）
// -----------------------------------------------------------------------------

// Stream 是流式 Command 与 Core 之间的双向通道。
//
// 典型使用（以 SSH 交互终端为例）：
//
//	func (h *ExecHandler) ExecuteStream(ctx context.Context, s sdk.Stream) error {
//		// 读取初始参数
//		var p struct{ Cmd string }
//		_ = s.Params(&p)
//
//		// 处理用户输入
//		go func() {
//			for {
//				select {
//				case <-ctx.Done():
//					return
//				case msg := <-s.Recv():
//					switch v := msg.(type) {
//					case *sdk.Stdin:
//						session.Write(v.Data)
//					case *sdk.Signal:
//						if v.Type == sdk.SignalCancel {
//							return
//						}
//					}
//				}
//			}
//		}()
//
//		// 输出
//		s.Stdout([]byte("hello"))
//		return s.Finish(nil, 0)
//	}
type Stream interface {
	// Context 返回本次流的 Context，取消时表示 Core 或客户端已中止。
	Context() context.Context

	// AuditID 是本次调用的全局唯一标识（同 ExecuteRequest.AuditID）。
	AuditID() string

	// Caller 记录调用来源，供审计与权限判定。
	Caller() Caller

	// Confirmed 标识 Dangerous Command 是否已经用户确认。
	Confirmed() bool

	// Params 将首帧携带的 Command 参数解码到 dst。
	// dst 通常是插件自定义结构体的指针。
	Params(dst any) error

	// RawParams 返回原始参数字节。
	RawParams() json.RawMessage

	// Connection 返回 Core 分配的连接（若 CommandSpec.ConnectionType 非空）。
	Connection() *Connection

	// Recv 返回一个只读通道，Core / 客户端发来的输入通过它推送。
	// 通道关闭表示客户端已 Close 输入端；ctx 取消同样会关闭。
	Recv() <-chan Incoming

	// Stdout / Stderr 向客户端发送字节流。
	Stdout(data []byte) error
	Stderr(data []byte) error

	// Event 发送一条结构化事件（例如进度、状态迁移）。
	// v 会被 json.Marshal 后作为事件负载发送。
	Event(v any) error

	// Finish 结束本次流。
	//   - finalData 可选，作为最终结构化结果
	//   - exitCode 用于外部进程包装类 Command；0 表示成功
	//
	// 返回 nil 表示成功结束；非 nil 表示失败（等价于 CommandHandler 返回 error）。
	Finish(finalData any, exitCode int) error
}

// -----------------------------------------------------------------------------
// Incoming（客户端 → 插件 的入站消息）
// -----------------------------------------------------------------------------

// Incoming 是客户端 / Core 通过 Stream 发来的入站事件。
// 使用类型断言区分：*Stdin / *Signal。
type Incoming interface {
	incoming()
}

// Stdin 向 Command 提供标准输入（例如 SSH 交互）。
type Stdin struct {
	Data []byte
	At   time.Time
}

func (*Stdin) incoming() {}

// Signal 是控制信号，例如取消 / SIGINT / 终端 resize。
type Signal struct {
	Type    SignalType
	Payload json.RawMessage // 例：WINCH 时的 rows/cols
	At      time.Time
}

func (*Signal) incoming() {}

// SignalType 定义控制信号类型。
type SignalType int

const (
	SignalUnspecified SignalType = iota
	SignalCancel                 // 取消，等价于 context cancel
	SignalInt                    // SIGINT
	SignalTerm                   // SIGTERM
	SignalKill                   // SIGKILL
	SignalWinch                  // 终端尺寸变化
)

func (s SignalType) String() string {
	switch s {
	case SignalCancel:
		return "cancel"
	case SignalInt:
		return "int"
	case SignalTerm:
		return "term"
	case SignalKill:
		return "kill"
	case SignalWinch:
		return "winch"
	default:
		return "unspecified"
	}
}
