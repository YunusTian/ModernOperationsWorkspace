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
// Client：把 pb.PluginClient 包成 sdk.Plugin
// -----------------------------------------------------------------------------

// client 是 Core 侧的 sdk.Plugin 适配层：把远程插件当作本地 Plugin 使用。
type client struct {
	cc pb.PluginClient

	// metaCache 缓存 Metadata()，避免每次都走 gRPC
	metaOnce sync.Once
	meta     sdk.Metadata
	cmds     []sdk.CommandHandler
}

// NewClient 构造一个 sdk.Plugin，其所有调用都会转发到远程 gRPC 服务。
func NewClient(cc pb.PluginClient) sdk.Plugin {
	return &client{cc: cc}
}

func (c *client) Metadata() sdk.Metadata {
	c.metaOnce.Do(c.loadMetadata)
	return c.meta
}

func (c *client) Commands() []sdk.CommandHandler {
	c.metaOnce.Do(c.loadMetadata)
	return c.cmds
}

func (c *client) loadMetadata() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.cc.Metadata(ctx, &pb.MetadataRequest{})
	if err != nil || resp == nil || resp.Metadata == nil {
		return
	}
	m := resp.Metadata
	c.meta = sdk.Metadata{
		ID:                 m.Id,
		Name:               m.Name,
		Version:            m.Version,
		Author:             m.Author,
		Description:        m.Description,
		Homepage:           m.Homepage,
		License:            m.License,
		CoreVersion:        m.CoreVersion,
		PluginDependencies: m.PluginDependencies,
		ConnectionTypes:    resp.ConnectionTypes,
	}
	for _, r := range resp.Recipes {
		c.meta.Recipes = append(c.meta.Recipes, sdk.RecipeSpec{
			ID:          r.Id,
			Description: r.Description,
			Permission:  permFromProto(r.Permission),
			CommandIDs:  r.CommandIds,
			Tags:        r.Tags,
		})
	}
	for _, w := range resp.Workflows {
		c.meta.Workflows = append(c.meta.Workflows, sdk.WorkflowSpec{
			ID:          w.Id,
			Description: w.Description,
			Permission:  permFromProto(w.Permission),
			Tags:        w.Tags,
		})
	}
	for _, cd := range resp.Commands {
		c.cmds = append(c.cmds, &remoteCommand{cc: c.cc, spec: sdk.CommandSpec{
			ID:             cd.Id,
			Description:    cd.Description,
			Permission:     permFromProto(cd.Permission),
			Streaming:      cd.Streaming,
			InputSchema:    json.RawMessage(cd.InputSchema),
			OutputSchema:   json.RawMessage(cd.OutputSchema),
			ConnectionType: cd.ConnectionType,
			DefaultTimeout: durFromProto(cd.DefaultTimeout),
			Idempotent:     cd.Idempotent,
			Tags:           cd.Tags,
		}})
	}
}

func (c *client) Init(ctx context.Context, req sdk.InitRequest) error {
	st, _ := jsonToStruct(req.Settings)
	resp, err := c.cc.Init(ctx, &pb.InitRequest{
		Settings:    st,
		CoreVersion: req.CoreVersion,
		DataDir:     req.DataDir,
	})
	if err != nil {
		return err
	}
	if resp != nil && resp.Error != nil {
		return errorFromProto(resp.Error)
	}
	return nil
}

func (c *client) Shutdown(ctx context.Context) error {
	_, err := c.cc.Shutdown(ctx, &pb.ShutdownRequest{})
	return err
}

func (c *client) HealthCheck(ctx context.Context) sdk.HealthStatus {
	resp, err := c.cc.HealthCheck(ctx, &pb.HealthCheckRequest{})
	if err != nil || resp == nil {
		return sdk.StatusUnknown
	}
	return healthFromProto(resp.Status)
}

// -----------------------------------------------------------------------------
// remoteCommand
// -----------------------------------------------------------------------------

type remoteCommand struct {
	cc   pb.PluginClient
	spec sdk.CommandSpec
}

func (r *remoteCommand) Spec() sdk.CommandSpec { return r.spec }

func (r *remoteCommand) Execute(ctx context.Context, req *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	params, _ := jsonToStruct(req.Params)
	resp, err := r.cc.Execute(ctx, &pb.ExecuteRequest{
		AuditId:      req.AuditID,
		CommandId:    r.spec.ID,
		Params:       params,
		ConnectionId: connectionIDOf(req.Connection),
		Caller:       callerToProto(req.Caller),
		Timeout:      durToProto(req.Timeout),
		Confirmed:    req.Confirmed,
	})
	if err != nil {
		return nil, err
	}
	if !resp.Ok {
		return nil, errorFromProto(resp.Error)
	}
	return &sdk.ExecuteResponse{
		Data:       structToJSON(resp.Data),
		Attributes: resp.Attributes,
	}, nil
}

func connectionIDOf(c *sdk.Connection) string {
	if c == nil {
		return ""
	}
	return c.ID
}

// ExecuteStream 客户端侧目前只提供最小占位实现：
// 建流 → 发 Start → 让上层 sdk.Stream 桥接。此实现将在 Connection RFC 完成后细化。
func (r *remoteCommand) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	stream, err := r.cc.ExecuteStream(ctx)
	if err != nil {
		return err
	}
	params, _ := jsonToStruct(s.RawParams())
	if err := stream.Send(&pb.ExecuteStreamMessage{
		Payload: &pb.ExecuteStreamMessage_Start{
			Start: &pb.ExecuteRequest{
				AuditId:   s.AuditID(),
				CommandId: r.spec.ID,
				Params:    params,
				Caller:    callerToProto(s.Caller()),
				Confirmed: s.Confirmed(),
			},
		},
	}); err != nil {
		return err
	}

	// 双向泵：sdk.Stream ↔ gRPC 流
	cs := &clientStream{ctx: ctx, s: s, stream: stream}
	go cs.pumpUpstream()  // s.Recv() → gRPC.Send
	return cs.pumpDownstream() // gRPC.Recv → s.Stdout/Stderr/Event/Finish
}

// -----------------------------------------------------------------------------
// clientStream：Core → Plugin 方向的双向桥
// -----------------------------------------------------------------------------

type clientStream struct {
	ctx      context.Context
	s        sdk.Stream
	stream   pb.Plugin_ExecuteStreamClient
	finished atomic.Bool
}

func (c *clientStream) pumpUpstream() {
	for msg := range c.s.Recv() {
		switch v := msg.(type) {
		case *sdk.Stdin:
			_ = c.stream.Send(&pb.ExecuteStreamMessage{
				Payload: &pb.ExecuteStreamMessage_Stdin{Stdin: &pb.Stdin{Data: v.Data}},
			})
		case *sdk.Signal:
			var payload *structpb.Struct
			if len(v.Payload) > 0 {
				payload, _ = jsonToStruct(v.Payload)
			}
			_ = c.stream.Send(&pb.ExecuteStreamMessage{
				Payload: &pb.ExecuteStreamMessage_Signal{Signal: &pb.Signal{
					Type: signalTypeToProto(v.Type), Payload: payload,
				}},
			})
		}
	}
	_ = c.stream.Send(&pb.ExecuteStreamMessage{
		Payload: &pb.ExecuteStreamMessage_Close{Close: &pb.Close{}},
	})
}

func (c *clientStream) pumpDownstream() error {
	for {
		msg, err := c.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch p := msg.Payload.(type) {
		case *pb.ExecuteStreamMessage_Chunk:
			ch := p.Chunk
			switch ch.Stream {
			case pb.ChunkStream_CHUNK_STREAM_STDOUT:
				_ = c.s.Stdout(ch.Data)
			case pb.ChunkStream_CHUNK_STREAM_STDERR:
				_ = c.s.Stderr(ch.Data)
			case pb.ChunkStream_CHUNK_STREAM_STRUCTURED:
				if ch.Event != nil {
					_ = c.s.Event(ch.Event.AsMap())
				}
			}
		case *pb.ExecuteStreamMessage_Final:
			c.finished.Store(true)
			// 将 Final 转成 Stream.Finish；Handler 已经结束就跳过
			if p.Final != nil && p.Final.Error != nil {
				return errorFromProto(p.Final.Error)
			}
			// 若插件带回了 final data，通过 Event 一次性投递
			if p.Final != nil && p.Final.Data != nil {
				_ = c.s.Event(p.Final.Data.AsMap())
			}
			return nil
		}
		_ = msg
	}
}
