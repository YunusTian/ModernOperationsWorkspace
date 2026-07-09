package grpcbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/mow/mow/sdk"
	pb "github.com/mow/mow/sdk/proto"
)

// -----------------------------------------------------------------------------
// serverStream 实现 sdk.Stream
// -----------------------------------------------------------------------------

// serverStream 把 gRPC 双向流适配为 sdk.Stream，暴露给 CommandHandler 使用。
type serverStream struct {
	ctx    context.Context
	stream pb.Plugin_ExecuteStreamServer
	start  *pb.ExecuteRequest

	rawParams json.RawMessage
	conn      *sdk.Connection

	// incoming 提供给 Handler 消费入站事件（Stdin / Signal）
	incoming chan sdk.Incoming

	// sendMu 保证并发 send 安全
	sendMu   sync.Mutex
	seq      atomic.Uint64
	finished atomic.Bool
}

func newServerStream(ctx context.Context, s pb.Plugin_ExecuteStreamServer, start *pb.ExecuteRequest, params json.RawMessage, conn *sdk.Connection) *serverStream {
	return &serverStream{
		ctx:       ctx,
		stream:    s,
		start:     start,
		rawParams: params,
		conn:      conn,
		incoming:  make(chan sdk.Incoming, 16),
	}
}

// pumpRecv 在独立 goroutine 中把 gRPC 入站消息推送到 incoming。
// 遇到 EOF / Close / Cancel 时关闭 incoming 通道。
func (s *serverStream) pumpRecv() {
	defer close(s.incoming)
	for {
		msg, err := s.stream.Recv()
		if err != nil {
			return
		}
		switch p := msg.Payload.(type) {
		case *pb.ExecuteStreamMessage_Stdin:
			select {
			case s.incoming <- &sdk.Stdin{
				Data: p.Stdin.GetData(),
				At:   time.Now(),
			}:
			case <-s.ctx.Done():
				return
			}
		case *pb.ExecuteStreamMessage_Signal:
			var payload json.RawMessage
			if p.Signal.GetPayload() != nil {
				payload = structToJSON(p.Signal.GetPayload())
			}
			select {
			case s.incoming <- &sdk.Signal{
				Type:    signalTypeFromProto(p.Signal.GetType()),
				Payload: payload,
				At:      time.Now(),
			}:
			case <-s.ctx.Done():
				return
			}
		case *pb.ExecuteStreamMessage_Close:
			return
		}
	}
}

// -----------------------------------------------------------------------------
// sdk.Stream 接口实现
// -----------------------------------------------------------------------------

func (s *serverStream) Context() context.Context { return s.ctx }
func (s *serverStream) AuditID() string          { return s.start.GetAuditId() }
func (s *serverStream) Caller() sdk.Caller       { return callerFromProto(s.start.GetCaller()) }
func (s *serverStream) Confirmed() bool          { return s.start.GetConfirmed() }
func (s *serverStream) RawParams() json.RawMessage {
	return s.rawParams
}

func (s *serverStream) Params(dst any) error {
	if len(s.rawParams) == 0 {
		return nil
	}
	return json.Unmarshal(s.rawParams, dst)
}

// Connection 返回 Core 通过信封字段透传过来的连接（可为 nil）。
func (s *serverStream) Connection() *sdk.Connection { return s.conn }

func (s *serverStream) Recv() <-chan sdk.Incoming { return s.incoming }

func (s *serverStream) Stdout(data []byte) error {
	return s.sendChunk(pb.ChunkStream_CHUNK_STREAM_STDOUT, data, nil)
}
func (s *serverStream) Stderr(data []byte) error {
	return s.sendChunk(pb.ChunkStream_CHUNK_STREAM_STDERR, data, nil)
}

func (s *serverStream) Event(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	st, err := jsonToStruct(b)
	if err != nil {
		return err
	}
	return s.sendChunk(pb.ChunkStream_CHUNK_STREAM_STRUCTURED, nil, st)
}

func (s *serverStream) Finish(finalData any, exitCode int) error {
	if s.finished.Swap(true) {
		return errors.New("sdk: stream already finished")
	}
	var raw json.RawMessage
	if finalData != nil {
		b, err := json.Marshal(finalData)
		if err != nil {
			return err
		}
		raw = b
	}
	return s.sendFinal(nil, int32(exitCode), raw)
}

// -----------------------------------------------------------------------------
// 底层发送
// -----------------------------------------------------------------------------

func (s *serverStream) sendChunk(kind pb.ChunkStream, data []byte, event *structpb.Struct) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	chunk := &pb.Chunk{
		Stream: kind,
		Data:   data,
		Event:  event,
		Seq:    s.seq.Add(1),
		Ts:     timeToProto(time.Now()),
	}
	return s.stream.Send(&pb.ExecuteStreamMessage{
		Payload: &pb.ExecuteStreamMessage_Chunk{Chunk: chunk},
	})
}

func (s *serverStream) sendFinal(err error, exitCode int32, data json.RawMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	final := &pb.Final{
		Ok:       err == nil,
		Error:    errorToProto(err),
		ExitCode: exitCode,
	}
	if len(data) > 0 {
		st, jerr := jsonToStruct(data)
		if jerr == nil {
			final.Data = st
		}
	}
	sendErr := s.stream.Send(&pb.ExecuteStreamMessage{
		Payload: &pb.ExecuteStreamMessage_Final{Final: final},
	})
	if sendErr != nil && !errors.Is(sendErr, io.EOF) {
		return sendErr
	}
	return nil
}
