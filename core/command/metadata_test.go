package command_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
)

// captureSink 记录所有 Finish 事件，供断言使用。
type captureSink struct {
	records []*command.AuditRecord
}

func (s *captureSink) Start(context.Context, *command.AuditRecord) {}
func (s *captureSink) Finish(_ context.Context, r *command.AuditRecord) {
	// 深拷贝 metadata 避免测试与中间件写入竞态
	cp := *r
	if r.Metadata != nil {
		m := make(map[string]any, len(r.Metadata))
		for k, v := range r.Metadata {
			m[k] = v
		}
		cp.Metadata = m
	}
	s.records = append(s.records, &cp)
}

// echoHandler 直接把 Params + Metadata 汇报到 response.Data。
type sensitiveHandler struct{}

func (sensitiveHandler) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:         "login",
		Permission: sdk.PermExecute,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"user":     {"type":"string"},
				"password": {"type":"string","x-mow-sensitive":true}
			}
		}`),
	}
}
func (sensitiveHandler) Execute(_ context.Context, r *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	// Handler 收到的 Params 应是原始（未脱敏）值；用来对比 AuditRecord。
	return &sdk.ExecuteResponse{Data: r.Params}, nil
}
func (sensitiveHandler) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}

// traceMiddleware 演示 metadata 用法：在链头写入 trace.id 供后续读取。
func traceMiddleware(traceID string) command.Middleware {
	return func(next command.HandlerFunc) command.HandlerFunc {
		return func(ctx context.Context, inv *command.Invocation) (*command.Response, error) {
			inv.Request.SetMetadata("trace.id", traceID)
			return next(ctx, inv)
		}
	}
}

func newSensitiveEngine(t *testing.T, sink command.AuditSink, mws ...command.Middleware) *command.Engine {
	t.Helper()
	pm := plugin.NewManager(plugin.Options{})
	if err := pm.Register(&fakePlugin{id: "app", cmds: []sdk.CommandHandler{sensitiveHandler{}}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := pm.Enable(context.Background(), "app", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	return command.New(command.Options{
		Manager:          pm,
		Audit:            sink,
		Confirm:          command.AllowConfirmer{},
		ExtraMiddlewares: mws,
	})
}

func TestAuditRecord_ParamsRedacted(t *testing.T) {
	sink := &captureSink{}
	eng := newSensitiveEngine(t, sink)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID:  "app",
		CommandID: "login",
		Params:    json.RawMessage(`{"user":"alice","password":"hunter2"}`),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sink.records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(sink.records))
	}
	rec := sink.records[0]
	if strings.Contains(string(rec.Params), "hunter2") {
		t.Errorf("secret leaked into audit params: %s", rec.Params)
	}
	if !strings.Contains(string(rec.Params), `"password":"***"`) {
		t.Errorf("password should be masked with ***; got: %s", rec.Params)
	}
	// user 字段应保持原样
	if !strings.Contains(string(rec.Params), `"user":"alice"`) {
		t.Errorf("non-sensitive fields should be preserved; got: %s", rec.Params)
	}
}

func TestMetadata_MiddlewareToAudit(t *testing.T) {
	sink := &captureSink{}
	eng := newSensitiveEngine(t, sink, traceMiddleware("t-123"))

	_, err := eng.Run(context.Background(), command.Request{
		PluginID:  "app",
		CommandID: "login",
		Params:    json.RawMessage(`{"user":"alice","password":"x"}`),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rec := sink.records[0]
	if rec.Metadata == nil {
		t.Fatal("audit record should carry metadata")
	}
	if rec.Metadata["trace.id"] != "t-123" {
		t.Errorf("trace.id mismatch: %v", rec.Metadata["trace.id"])
	}
}

func TestMetadata_RequestGetSet(t *testing.T) {
	var r command.Request
	if v := r.GetMetadata("x"); v != nil {
		t.Errorf("empty request should return nil, got %v", v)
	}
	r.SetMetadata("x", 1)
	r.SetMetadata("y", "abc")
	if r.GetMetadata("x").(int) != 1 || r.GetMetadata("y").(string) != "abc" {
		t.Errorf("get/set mismatch: %+v", r.Metadata)
	}
}
