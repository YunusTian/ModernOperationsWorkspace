package grpcbridge

import (
	"context"
	"time"

	"github.com/mow/mow/sdk"
	pb "github.com/mow/mow/sdk/proto"
)

// -----------------------------------------------------------------------------
// Server：把 sdk.Plugin 包装成 pb.PluginServer
// -----------------------------------------------------------------------------

// server 是 gRPC 服务端实现；由插件进程加载后监听 gRPC 请求。
// 一个进程一个 server 实例。
type server struct {
	pb.UnimplementedPluginServer

	impl sdk.Plugin

	// cmdByID 缓存 Plugin.Commands() 的结果，避免每次调用都反射。
	cmdByID map[string]sdk.CommandHandler
}

// NewServer 构造一个 gRPC 服务端，包装用户提供的 sdk.Plugin。
func NewServer(p sdk.Plugin) pb.PluginServer {
	cmds := map[string]sdk.CommandHandler{}
	for _, h := range p.Commands() {
		cmds[h.Spec().ID] = h
	}
	return &server{impl: p, cmdByID: cmds}
}

// -----------------------------------------------------------------------------
// Metadata
// -----------------------------------------------------------------------------

func (s *server) Metadata(ctx context.Context, _ *pb.MetadataRequest) (*pb.MetadataResponse, error) {
	m := s.impl.Metadata()

	resp := &pb.MetadataResponse{
		Metadata: &pb.PluginMetadata{
			Id:                 m.ID,
			Name:               m.Name,
			Version:            m.Version,
			Author:             m.Author,
			Description:        m.Description,
			Homepage:           m.Homepage,
			License:            m.License,
			CoreVersion:        m.CoreVersion,
			PluginDependencies: m.PluginDependencies,
		},
		ConnectionTypes: m.ConnectionTypes,
	}

	for _, h := range s.impl.Commands() {
		resp.Commands = append(resp.Commands, commandSpecToProto(h.Spec()))
	}
	for _, r := range m.Recipes {
		resp.Recipes = append(resp.Recipes, recipeSpecToProto(r))
	}
	for _, w := range m.Workflows {
		resp.Workflows = append(resp.Workflows, workflowSpecToProto(w))
	}
	return resp, nil
}

func commandSpecToProto(s sdk.CommandSpec) *pb.CommandDefinition {
	return &pb.CommandDefinition{
		Id:             s.ID,
		Description:    s.Description,
		Permission:     permToProto(s.Permission),
		Streaming:      s.Streaming,
		InputSchema:    string(s.InputSchema),
		OutputSchema:   string(s.OutputSchema),
		ConnectionType: s.ConnectionType,
		DefaultTimeout: durToProto(s.DefaultTimeout),
		Idempotent:     s.Idempotent,
		Tags:           s.Tags,
	}
}

func recipeSpecToProto(r sdk.RecipeSpec) *pb.RecipeDefinition {
	return &pb.RecipeDefinition{
		Id:          r.ID,
		Description: r.Description,
		Permission:  permToProto(r.Permission),
		CommandIds:  r.CommandIDs,
		Tags:        r.Tags,
	}
}

func workflowSpecToProto(w sdk.WorkflowSpec) *pb.WorkflowDefinition {
	return &pb.WorkflowDefinition{
		Id:          w.ID,
		Description: w.Description,
		Permission:  permToProto(w.Permission),
		Tags:        w.Tags,
	}
}

// -----------------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------------

func (s *server) Init(ctx context.Context, req *pb.InitRequest) (*pb.InitResponse, error) {
	err := s.impl.Init(ctx, sdk.InitRequest{
		Settings:    structToJSON(req.GetSettings()),
		CoreVersion: req.GetCoreVersion(),
		DataDir:     req.GetDataDir(),
	})
	return &pb.InitResponse{Error: errorToProto(err)}, nil
}

func (s *server) Shutdown(ctx context.Context, _ *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	_ = s.impl.Shutdown(ctx)
	return &pb.ShutdownResponse{}, nil
}

func (s *server) HealthCheck(ctx context.Context, _ *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	st := s.impl.HealthCheck(ctx)
	return &pb.HealthCheckResponse{
		Status:  healthToProto(st),
		Message: st.String(),
	}, nil
}

// -----------------------------------------------------------------------------
// Execute（一次性）
// -----------------------------------------------------------------------------

func (s *server) Execute(ctx context.Context, req *pb.ExecuteRequest) (*pb.ExecuteResponse, error) {
	h, ok := s.cmdByID[req.GetCommandId()]
	if !ok {
		return &pb.ExecuteResponse{
			Ok:    false,
			Error: errorToProto(sdk.NewError("COMMAND_NOT_FOUND", "unknown command: "+req.GetCommandId(), nil)),
		}, nil
	}

	if to := durFromProto(req.GetTimeout()); to > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}

	start := time.Now()
	paramsJSON, conn := splitConnectionFromParams(req.GetParams())
	resp, err := h.Execute(ctx, &sdk.ExecuteRequest{
		AuditID:    req.GetAuditId(),
		Params:     paramsJSON,
		Connection: conn,
		Caller:     callerFromProto(req.GetCaller()),
		Timeout:    durFromProto(req.GetTimeout()),
		Confirmed:  req.GetConfirmed(),
	})
	out := &pb.ExecuteResponse{Duration: durToProto(time.Since(start))}
	if err != nil {
		out.Ok = false
		out.Error = errorToProto(err)
		return out, nil
	}
	out.Ok = true
	if resp != nil {
		if data, err := jsonToStruct(resp.Data); err == nil {
			out.Data = data
		}
		out.Attributes = resp.Attributes
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// ExecuteStream（双向流）
// -----------------------------------------------------------------------------

func (s *server) ExecuteStream(stream pb.Plugin_ExecuteStreamServer) error {
	// 首帧必须是 Start
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return sdk.NewError("PROTOCOL_VIOLATION", "first stream message must be Start", nil)
	}

	h, ok := s.cmdByID[start.GetCommandId()]
	if !ok {
		return sdk.NewError("COMMAND_NOT_FOUND", "unknown command: "+start.GetCommandId(), nil)
	}

	ctx := stream.Context()
	if to := durFromProto(start.GetTimeout()); to > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}

	rawParams, conn := splitConnectionFromParams(start.GetParams())
	s3 := newServerStream(ctx, stream, start, rawParams, conn)
	go s3.pumpRecv()

	execErr := h.ExecuteStream(ctx, s3)

	// 若 Handler 未主动调用 Finish，服务端补一次 Final。
	if !s3.finished.Load() {
		return s3.sendFinal(execErr, 0, nil)
	}
	return execErr
}
