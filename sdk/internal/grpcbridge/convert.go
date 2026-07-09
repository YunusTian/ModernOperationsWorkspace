// Package grpcbridge 桥接 SDK 的 Go 抽象与 gRPC / hashicorp/go-plugin 层。
//
// 本包对外不导出（internal），任何调用都应通过 sdk.Serve / sdk 提供的公共 API。
//
//   Plugin (Go struct) ─┐                          ┌─ Plugin (Go interface)
//                       │                          │
//              server.go│                          │client.go
//                       ▼                          ▲
//   PluginServer  ──── gRPC (Unix / TCP) ────  PluginClient
//                            │
//               convert.go / stream.go
package grpcbridge

import (
	"encoding/json"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mow/mow/sdk"
	pb "github.com/mow/mow/sdk/proto"
)

// -----------------------------------------------------------------------------
// JSON <-> structpb
// -----------------------------------------------------------------------------

// jsonToStruct 将任意 JSON 转成 structpb.Struct。
// 传入 nil / 空 → 返回 nil。
func jsonToStruct(raw json.RawMessage) (*structpb.Struct, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return structpb.NewStruct(m)
}

// structToJSON 将 structpb.Struct 序列化回 JSON RawMessage；nil 返回 nil。
func structToJSON(s *structpb.Struct) json.RawMessage {
	if s == nil {
		return nil
	}
	b, err := json.Marshal(s.AsMap())
	if err != nil {
		return nil
	}
	return b
}

// -----------------------------------------------------------------------------
// Permission
// -----------------------------------------------------------------------------

func permToProto(p sdk.Permission) pb.Permission {
	switch p {
	case sdk.PermRead:
		return pb.Permission_PERMISSION_READ
	case sdk.PermWrite:
		return pb.Permission_PERMISSION_WRITE
	case sdk.PermExecute:
		return pb.Permission_PERMISSION_EXECUTE
	case sdk.PermDangerous:
		return pb.Permission_PERMISSION_DANGEROUS
	default:
		return pb.Permission_PERMISSION_UNSPECIFIED
	}
}

func permFromProto(p pb.Permission) sdk.Permission {
	switch p {
	case pb.Permission_PERMISSION_READ:
		return sdk.PermRead
	case pb.Permission_PERMISSION_WRITE:
		return sdk.PermWrite
	case pb.Permission_PERMISSION_EXECUTE:
		return sdk.PermExecute
	case pb.Permission_PERMISSION_DANGEROUS:
		return sdk.PermDangerous
	default:
		return sdk.PermUnspecified
	}
}

// -----------------------------------------------------------------------------
// CallerType
// -----------------------------------------------------------------------------

func callerTypeToProto(t sdk.CallerType) pb.CallerType {
	switch t {
	case sdk.CallerCLI:
		return pb.CallerType_CALLER_TYPE_CLI
	case sdk.CallerDesktop:
		return pb.CallerType_CALLER_TYPE_DESKTOP
	case sdk.CallerAPI:
		return pb.CallerType_CALLER_TYPE_API
	case sdk.CallerAI:
		return pb.CallerType_CALLER_TYPE_AI
	case sdk.CallerWorkflow:
		return pb.CallerType_CALLER_TYPE_WORKFLOW
	case sdk.CallerRecipe:
		return pb.CallerType_CALLER_TYPE_RECIPE
	default:
		return pb.CallerType_CALLER_TYPE_UNSPECIFIED
	}
}

func callerTypeFromProto(t pb.CallerType) sdk.CallerType {
	switch t {
	case pb.CallerType_CALLER_TYPE_CLI:
		return sdk.CallerCLI
	case pb.CallerType_CALLER_TYPE_DESKTOP:
		return sdk.CallerDesktop
	case pb.CallerType_CALLER_TYPE_API:
		return sdk.CallerAPI
	case pb.CallerType_CALLER_TYPE_AI:
		return sdk.CallerAI
	case pb.CallerType_CALLER_TYPE_WORKFLOW:
		return sdk.CallerWorkflow
	case pb.CallerType_CALLER_TYPE_RECIPE:
		return sdk.CallerRecipe
	default:
		return sdk.CallerUnspecified
	}
}

func callerToProto(c sdk.Caller) *pb.Caller {
	return &pb.Caller{
		Type:          callerTypeToProto(c.Type),
		User:          c.User,
		SessionId:     c.SessionID,
		ParentAuditId: c.ParentAuditID,
	}
}

func callerFromProto(c *pb.Caller) sdk.Caller {
	if c == nil {
		return sdk.Caller{}
	}
	return sdk.Caller{
		Type:          callerTypeFromProto(c.Type),
		User:          c.User,
		SessionID:     c.SessionId,
		ParentAuditID: c.ParentAuditId,
	}
}

// -----------------------------------------------------------------------------
// Signal
// -----------------------------------------------------------------------------

func signalTypeToProto(t sdk.SignalType) pb.SignalType {
	switch t {
	case sdk.SignalCancel:
		return pb.SignalType_SIGNAL_TYPE_CANCEL
	case sdk.SignalInt:
		return pb.SignalType_SIGNAL_TYPE_INT
	case sdk.SignalTerm:
		return pb.SignalType_SIGNAL_TYPE_TERM
	case sdk.SignalKill:
		return pb.SignalType_SIGNAL_TYPE_KILL
	case sdk.SignalWinch:
		return pb.SignalType_SIGNAL_TYPE_WINCH
	default:
		return pb.SignalType_SIGNAL_TYPE_UNSPECIFIED
	}
}

func signalTypeFromProto(t pb.SignalType) sdk.SignalType {
	switch t {
	case pb.SignalType_SIGNAL_TYPE_CANCEL:
		return sdk.SignalCancel
	case pb.SignalType_SIGNAL_TYPE_INT:
		return sdk.SignalInt
	case pb.SignalType_SIGNAL_TYPE_TERM:
		return sdk.SignalTerm
	case pb.SignalType_SIGNAL_TYPE_KILL:
		return sdk.SignalKill
	case pb.SignalType_SIGNAL_TYPE_WINCH:
		return sdk.SignalWinch
	default:
		return sdk.SignalUnspecified
	}
}

// -----------------------------------------------------------------------------
// HealthStatus
// -----------------------------------------------------------------------------

func healthToProto(s sdk.HealthStatus) pb.HealthStatus {
	switch s {
	case sdk.StatusHealthy:
		return pb.HealthStatus_HEALTH_STATUS_HEALTHY
	case sdk.StatusDegraded:
		return pb.HealthStatus_HEALTH_STATUS_DEGRADED
	case sdk.StatusUnhealthy:
		return pb.HealthStatus_HEALTH_STATUS_UNHEALTHY
	default:
		return pb.HealthStatus_HEALTH_STATUS_UNSPECIFIED
	}
}

func healthFromProto(s pb.HealthStatus) sdk.HealthStatus {
	switch s {
	case pb.HealthStatus_HEALTH_STATUS_HEALTHY:
		return sdk.StatusHealthy
	case pb.HealthStatus_HEALTH_STATUS_DEGRADED:
		return sdk.StatusDegraded
	case pb.HealthStatus_HEALTH_STATUS_UNHEALTHY:
		return sdk.StatusUnhealthy
	default:
		return sdk.StatusUnknown
	}
}

// -----------------------------------------------------------------------------
// Duration / Time
// -----------------------------------------------------------------------------

func durToProto(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

func durFromProto(d *durationpb.Duration) time.Duration {
	if d == nil {
		return 0
	}
	return d.AsDuration()
}

func timeToProto(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func timeFromProto(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime()
}

// -----------------------------------------------------------------------------
// Error
// -----------------------------------------------------------------------------

func errorToProto(err error) *pb.Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*sdk.Error); ok && e != nil {
		details, _ := structpb.NewStruct(e.Details)
		return &pb.Error{
			Code:      e.Code,
			Message:   e.Message,
			Details:   details,
			Retryable: e.Retryable,
		}
	}
	// 兜底：包装为 UNKNOWN
	return &pb.Error{Code: "UNKNOWN", Message: err.Error()}
}

func errorFromProto(e *pb.Error) *sdk.Error {
	if e == nil {
		return nil
	}
	var details map[string]any
	if e.Details != nil {
		details = e.Details.AsMap()
	}
	return &sdk.Error{
		Code:      e.Code,
		Message:   e.Message,
		Details:   details,
		Retryable: e.Retryable,
	}
}

// -----------------------------------------------------------------------------
// Connection（占位）
// -----------------------------------------------------------------------------

// v0.1：Connection 目前不跨越 proto 边界传递；后续 Connection RFC 会补齐。
// 这里仅保留导入以说明未来会在此处扩展。
