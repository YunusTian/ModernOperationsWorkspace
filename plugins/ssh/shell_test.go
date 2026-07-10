package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// shell mock Stream
// -----------------------------------------------------------------------------

type shellMockStream struct {
	conn       *sdk.Connection
	paramsErr  error
	paramsData []byte
}

func (m *shellMockStream) Context() context.Context           { return context.Background() }
func (m *shellMockStream) AuditID() string                    { return "test-audit" }
func (m *shellMockStream) Caller() sdk.Caller                 { return sdk.Caller{Type: sdk.CallerCLI} }
func (m *shellMockStream) Confirmed() bool                    { return false }
func (m *shellMockStream) RawParams() json.RawMessage         { return nil }
func (m *shellMockStream) Connection() *sdk.Connection        { return m.conn }
func (m *shellMockStream) Recv() <-chan sdk.Incoming          { return nil }
func (m *shellMockStream) Stdout(data []byte) error           { return nil }
func (m *shellMockStream) Stderr(data []byte) error           { return nil }
func (m *shellMockStream) Event(v any) error                  { return nil }
func (m *shellMockStream) Finish(v any, code int) error       { return nil }

func (m *shellMockStream) Params(dst any) error {
	if m.paramsErr != nil {
		return m.paramsErr
	}
	if m.paramsData == nil {
		return nil
	}
	return json.Unmarshal(m.paramsData, dst)
}

// -----------------------------------------------------------------------------
// Spec
// -----------------------------------------------------------------------------

func TestShell_Spec(t *testing.T) {
	cmd := &shellCmd{}
	spec := cmd.Spec()
	if spec.ID != "shell" {
		t.Errorf("ID want shell, got %q", spec.ID)
	}
	if !spec.Streaming {
		t.Error("shell should be streaming")
	}
	if spec.ConnectionType != "ssh" {
		t.Errorf("ConnectionType want ssh, got %q", spec.ConnectionType)
	}
	if spec.Permission != sdk.PermExecute {
		t.Errorf("Permission want Execute, got %v", spec.Permission)
	}
	if spec.Description == "" {
		t.Error("Description should not be empty")
	}
}

// -----------------------------------------------------------------------------
// Execute → ErrNotSupported
// -----------------------------------------------------------------------------

func TestShell_Execute_ReturnsNotSupported(t *testing.T) {
	cmd := &shellCmd{}
	_, err := cmd.Execute(context.Background(), nil)
	if !errors.Is(err, sdk.ErrNotSupported) {
		t.Errorf("expected ErrNotSupported, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ExecuteStream — 无 Connection
// -----------------------------------------------------------------------------

func TestShell_ExecuteStream_NoConnection(t *testing.T) {
	cmd := &shellCmd{}
	stream := &shellMockStream{conn: nil}
	err := cmd.ExecuteStream(context.Background(), stream)
	if !errors.Is(err, sdk.ErrConnectionRequired) {
		t.Errorf("expected ErrConnectionRequired, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ExecuteStream — 参数解码失败
// -----------------------------------------------------------------------------

func TestShell_ExecuteStream_InvalidParams(t *testing.T) {
	cmd := &shellCmd{}
	conn := &sdk.Connection{ID: "test", Type: "ssh"}
	stream := &shellMockStream{
		conn:      conn,
		paramsErr: errors.New("bad json"),
	}
	err := cmd.ExecuteStream(context.Background(), stream)
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "PARAM_INVALID" {
		t.Errorf("expected PARAM_INVALID, got %v", err)
	}
}

// -----------------------------------------------------------------------------
// ExecuteStream — 空参数取默认值（term/rows/cols）
// 通过校验后走到 pool.Acquire，pool 为 nil 时 panic / error 均可接受
// （本用例只验证参数路径不 crash）
// -----------------------------------------------------------------------------

func TestShell_ExecuteStream_DefaultsParseOK(t *testing.T) {
	cmd := &shellCmd{}
	conn := &sdk.Connection{ID: "test", Type: "ssh"}
	stream := &shellMockStream{
		conn: conn,
		// 空参数 → 走默认值设置
	}
	// 参数解析成功后试图 resolveTarget → 会因缺少 metadata 报错，
	// 但只要能通过参数默认值分支即证明 params 逻辑无 crash。
	err := cmd.ExecuteStream(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error after param parsing (no metadata)")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *sdk.Error, got %T: %v", err, err)
	}
	// 要么 CONNECTION_INVALID（metadata 缺 host），要么 SSH_DIAL_FAILED
	if se.Code != "CONNECTION_INVALID" && se.Code != "SSH_DIAL_FAILED" {
		t.Errorf("unexpected error code: %q", se.Code)
	}
	// 关键：没有因为空 params 而 PARAM_INVALID 或 panic
	if se.Code == "PARAM_INVALID" {
		t.Error("empty params should use defaults, not fail validation")
	}
}

// -----------------------------------------------------------------------------
// ExecuteStream — ctx 取消
// -----------------------------------------------------------------------------

func TestShell_ExecuteStream_ContextCanceled(t *testing.T) {
	cmd := &shellCmd{}
	conn := &sdk.Connection{ID: "test", Type: "ssh"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	stream := &shellMockStream{conn: conn}
	err := cmd.ExecuteStream(ctx, stream)
	if err == nil {
		t.Fatal("expected error on canceled context")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *sdk.Error, got %T: %v", err, err)
	}
	if se.Code != "CANCELED" && se.Code != "CONNECTION_INVALID" && se.Code != "SSH_DIAL_FAILED" {
		t.Errorf("expected CANCELED/CONNECTION_INVALID, got %q", se.Code)
	}
}

// -----------------------------------------------------------------------------
// shellParams 默认值逻辑（通过 ExecuteStream 间接验证）
// -----------------------------------------------------------------------------

func TestShell_ExecuteStream_ExplicitParamsParse(t *testing.T) {
	cmd := &shellCmd{}
	conn := &sdk.Connection{ID: "test", Type: "ssh"}
	stream := &shellMockStream{
		conn: conn,
		paramsData: []byte(`{"term":"vt100","rows":40,"cols":120}`),
	}
	err := cmd.ExecuteStream(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error after param parsing")
	}
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *sdk.Error, got %T: %v", err, err)
	}
	// 显式参数解析成功，不应 PARAM_INVALID
	if se.Code == "PARAM_INVALID" {
		t.Error("valid explicit params should not fail validation")
	}
}

// -----------------------------------------------------------------------------
// 超时 context（参数解析后进入 resolveTarget / pool 阶段触发 cancel）
// -----------------------------------------------------------------------------

func TestShell_ExecuteStream_Timeout(t *testing.T) {
	cmd := &shellCmd{}
	conn := &sdk.Connection{ID: "test", Type: "ssh"}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Microsecond)
	defer cancel()

	stream := &shellMockStream{conn: conn}
	err := cmd.ExecuteStream(ctx, stream)
	if err == nil {
		t.Fatal("expected error after timeout")
	}
	// ctx 过期 → 应返回 sdk.Error 类型
	var se *sdk.Error
	if !errors.As(err, &se) {
		t.Fatalf("expected *sdk.Error, got %T: %v", err, err)
	}
}
