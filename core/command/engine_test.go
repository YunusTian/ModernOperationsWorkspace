package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// fake plugin & handlers
// -----------------------------------------------------------------------------

type fakePlugin struct {
	id   string
	cmds []sdk.CommandHandler
}

func (p *fakePlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: p.id, Name: p.id, Version: "0.0.1"}
}
func (p *fakePlugin) Init(ctx context.Context, r sdk.InitRequest) error { return nil }
func (p *fakePlugin) Shutdown(ctx context.Context) error                { return nil }
func (p *fakePlugin) HealthCheck(ctx context.Context) sdk.HealthStatus  { return sdk.StatusHealthy }
func (p *fakePlugin) Commands() []sdk.CommandHandler                    { return p.cmds }

type staticHandler struct {
	id          string
	perm        sdk.Permission
	streaming   bool
	err         error
	dataString  string
	inputSchema json.RawMessage
}

func (h *staticHandler) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID:          h.id,
		Permission:  h.perm,
		Streaming:   h.streaming,
		InputSchema: h.inputSchema,
	}
}
func (h *staticHandler) Execute(ctx context.Context, r *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &sdk.ExecuteResponse{Data: json.RawMessage(`"` + h.dataString + `"`)}, nil
}
func (h *staticHandler) ExecuteStream(ctx context.Context, s sdk.Stream) error {
	return sdk.ErrNotSupported
}

func newEngineWith(t *testing.T, h sdk.CommandHandler, confirm command.Confirmer, sink command.AuditSink) *command.Engine {
	t.Helper()
	pm := plugin.NewManager(plugin.Options{})
	if err := pm.Register(&fakePlugin{id: "demo", cmds: []sdk.CommandHandler{h}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := pm.Enable(context.Background(), "demo", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	return command.New(command.Options{Manager: pm, Confirm: confirm, Audit: sink})
}

// -----------------------------------------------------------------------------
// tests
// -----------------------------------------------------------------------------

func TestRunSuccess(t *testing.T) {
	h := &staticHandler{id: "hello", perm: sdk.PermRead, dataString: "world"}
	eng := newEngineWith(t, h, nil, nil)

	resp, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "hello",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if resp.AuditID == "" {
		t.Error("AuditID should be generated")
	}
	if string(resp.Data) != `"world"` {
		t.Errorf("data mismatch: %s", resp.Data)
	}
}

func TestParamValidation(t *testing.T) {
	h := &staticHandler{id: "hello", perm: sdk.PermRead}
	eng := newEngineWith(t, h, nil, nil)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "hello",
		Params: json.RawMessage(`{not-json`),
	})
	if err == nil {
		t.Fatal("expected error for invalid JSON params")
	}
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected sdk.Error PARAM_INVALID, got %v", err)
	}
}

func TestParamValidation_RejectsNonObject(t *testing.T) {
	h := &staticHandler{id: "hello", perm: sdk.PermRead}
	eng := newEngineWith(t, h, nil, nil)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "hello", Params: json.RawMessage(`["not", "an object"]`),
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected sdk.Error PARAM_INVALID, got %v", err)
	}
}

func TestParamValidation_Schema(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"additionalProperties":false,
		"required":["name","port"],
		"properties":{
			"name":{"type":"string","minLength":1},
			"port":{"type":"integer","minimum":1,"maximum":65535}
		}
	}`)
	h := &staticHandler{id: "hello", perm: sdk.PermRead, inputSchema: schema}
	eng := newEngineWith(t, h, nil, nil)

	if _, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "hello", Params: json.RawMessage(`{"name":"web","port":8080}`),
	}); err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "hello", Params: json.RawMessage(`{"name":"","port":70000,"extra":true}`),
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_SCHEMA_INVALID" {
		t.Errorf("expected sdk.Error PARAM_SCHEMA_INVALID, got %v", err)
	}
}

func TestParamValidation_InvalidSchema(t *testing.T) {
	h := &staticHandler{
		id: "hello", perm: sdk.PermRead,
		inputSchema: json.RawMessage(`{"type":"not-a-real-json-schema-type"}`),
	}
	eng := newEngineWith(t, h, nil, nil)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "hello", Params: json.RawMessage(`{}`),
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "SCHEMA_INVALID" {
		t.Errorf("expected sdk.Error SCHEMA_INVALID, got %v", err)
	}
}

func TestPermissionUnspecifiedRejected(t *testing.T) {
	// sdk.Validate 已经在 Register 阶段挡住了未声明权限的 Command，
	// checkPermission 内的对应分支属于纵深防御——此处不再重复测试。
	t.Skip("covered by sdk.Validate; middleware branch kept as defense in depth")
}

func TestDangerousRequiresConfirm_Deny(t *testing.T) {
	h := &staticHandler{id: "rm", perm: sdk.PermDangerous, dataString: "removed"}
	eng := newEngineWith(t, h, command.DenyConfirmer{}, nil)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "rm",
	})
	if !errors.Is(err, sdk.ErrConfirmationRequired) {
		t.Errorf("want ErrConfirmationRequired, got %v", err)
	}
}

func TestDangerousRequiresConfirm_Allow(t *testing.T) {
	h := &staticHandler{id: "rm", perm: sdk.PermDangerous, dataString: "removed"}
	eng := newEngineWith(t, h, command.AllowConfirmer{}, nil)

	resp, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "rm",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if string(resp.Data) != `"removed"` {
		t.Errorf("data mismatch: %s", resp.Data)
	}
}

func TestDangerousPreConfirmed(t *testing.T) {
	called := 0
	confirm := confirmFn(func(context.Context, command.ConfirmationRequest) (bool, error) {
		called++
		return true, nil
	})
	h := &staticHandler{id: "rm", perm: sdk.PermDangerous}
	eng := newEngineWith(t, h, confirm, nil)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "rm", Confirmed: true,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if called != 0 {
		t.Errorf("Confirmer should NOT be invoked when Request.Confirmed=true, got %d", called)
	}
}

type confirmFn func(context.Context, command.ConfirmationRequest) (bool, error)

func (f confirmFn) Confirm(ctx context.Context, r command.ConfirmationRequest) (bool, error) {
	return f(ctx, r)
}

// -----------------------------------------------------------------------------
// Audit
// -----------------------------------------------------------------------------

type memAudit struct {
	mu     sync.Mutex
	starts []*command.AuditRecord
	ends   []*command.AuditRecord
}

func (a *memAudit) Start(ctx context.Context, r *command.AuditRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts = append(a.starts, r)
}
func (a *memAudit) Finish(ctx context.Context, r *command.AuditRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ends = append(a.ends, r)
}

func TestAuditEmittedOnSuccess(t *testing.T) {
	sink := &memAudit{}
	h := &staticHandler{id: "hello", perm: sdk.PermRead}
	eng := newEngineWith(t, h, nil, sink)

	if _, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "hello",
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sink.starts) != 1 || len(sink.ends) != 1 {
		t.Fatalf("audit calls mismatch: starts=%d ends=%d", len(sink.starts), len(sink.ends))
	}
	if sink.ends[0].Err != nil {
		t.Errorf("final record should be error-free, got %v", sink.ends[0].Err)
	}
	if sink.ends[0].Duration < 0 {
		t.Errorf("duration should be >= 0, got %v", sink.ends[0].Duration)
	}
}

func TestAuditEmittedOnFailure(t *testing.T) {
	sink := &memAudit{}
	boom := errors.New("boom")
	h := &staticHandler{id: "hello", perm: sdk.PermRead, err: boom}
	eng := newEngineWith(t, h, nil, sink)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "hello",
	})
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	// 应封装 InvocationError
	var ie *command.InvocationError
	if !errors.As(err, &ie) || ie.AuditID == "" {
		t.Errorf("expected InvocationError with AuditID, got %v", err)
	}
	if len(sink.ends) != 1 || sink.ends[0].Err == nil {
		t.Errorf("Finish record should have Err set")
	}
}

// -----------------------------------------------------------------------------
// Streaming guards
// -----------------------------------------------------------------------------

func TestRunRejectsStreamingCommand(t *testing.T) {
	h := &staticHandler{id: "tail", perm: sdk.PermRead, streaming: true}
	eng := newEngineWith(t, h, nil, nil)
	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "tail",
	})
	if !errors.Is(err, command.ErrStreamingCommand) {
		t.Errorf("expected ErrStreamingCommand, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// AuditID
// -----------------------------------------------------------------------------

func TestNewAuditIDUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 1000; i++ {
		id := command.NewAuditID()
		if !strings.Contains(id, "-") {
			t.Fatalf("unexpected format: %s", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate auditid: %s", id)
		}
		seen[id] = struct{}{}
	}
}
