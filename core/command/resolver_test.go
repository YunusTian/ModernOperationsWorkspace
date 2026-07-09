package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
)

// connHandler 断言 Handler 收到的 Connection.ID 与 wantID 一致。
type connHandler struct {
	id     string
	perm   sdk.Permission
	connCh chan string
}

func (h *connHandler) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{ID: h.id, Permission: h.perm, ConnectionType: "ssh"}
}
func (h *connHandler) Execute(ctx context.Context, r *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if r.Connection == nil {
		h.connCh <- ""
	} else {
		h.connCh <- r.Connection.ID
	}
	return &sdk.ExecuteResponse{Data: json.RawMessage(`{}`)}, nil
}
func (h *connHandler) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}

// stubResolver 记录最近一次 Open 的 target。
type stubResolver struct {
	last  string
	calls int
	err   error
	conn  *sdk.Connection
}

func (s *stubResolver) Open(ctx context.Context, id string) (*sdk.Connection, error) {
	s.last = id
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if s.conn != nil {
		return s.conn, nil
	}
	return &sdk.Connection{ID: id, Type: "ssh"}, nil
}

func newResolveEngine(t *testing.T, h sdk.CommandHandler, r command.ConnectionResolver) *command.Engine {
	t.Helper()
	pm := plugin.NewManager(plugin.Options{})
	if err := pm.Register(&fakePlugin{id: "demo", cmds: []sdk.CommandHandler{h}}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := pm.Enable(context.Background(), "demo", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	return command.New(command.Options{
		Manager:  pm,
		Resolver: r,
		Confirm:  command.AllowConfirmer{},
	})
}

func TestResolver_UsedWhenTargetIDProvided(t *testing.T) {
	ch := make(chan string, 1)
	h := &connHandler{id: "exec", perm: sdk.PermExecute, connCh: ch}
	res := &stubResolver{}
	eng := newResolveEngine(t, h, res)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "exec", TargetID: "srv01",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.calls != 1 || res.last != "srv01" {
		t.Errorf("resolver calls=%d last=%q", res.calls, res.last)
	}
	if got := <-ch; got != "srv01" {
		t.Errorf("handler saw connection.ID=%q, want srv01", got)
	}
}

func TestResolver_ExplicitConnectionSkipsResolver(t *testing.T) {
	ch := make(chan string, 1)
	h := &connHandler{id: "exec", perm: sdk.PermExecute, connCh: ch}
	res := &stubResolver{}
	eng := newResolveEngine(t, h, res)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "exec",
		Connection: &sdk.Connection{ID: "manual", Type: "ssh"},
		TargetID:   "should-be-ignored",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.calls != 0 {
		t.Errorf("resolver should NOT be called; got calls=%d", res.calls)
	}
	if got := <-ch; got != "manual" {
		t.Errorf("handler saw connection.ID=%q, want manual", got)
	}
}

func TestResolver_MissingRequired(t *testing.T) {
	h := &connHandler{id: "exec", perm: sdk.PermExecute, connCh: make(chan string, 1)}
	eng := newResolveEngine(t, h, nil)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "exec", // 没有 TargetID / Connection
	})
	if !errors.Is(err, sdk.ErrConnectionRequired) {
		t.Errorf("want ErrConnectionRequired, got %v", err)
	}
}

func TestResolver_TargetIDButNoResolver(t *testing.T) {
	h := &connHandler{id: "exec", perm: sdk.PermExecute, connCh: make(chan string, 1)}
	eng := newResolveEngine(t, h, nil)

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "exec", TargetID: "x",
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "RESOLVER_MISSING" {
		t.Errorf("want RESOLVER_MISSING, got %v", err)
	}
}

func TestResolver_ResolveError(t *testing.T) {
	ch := make(chan string, 1)
	h := &connHandler{id: "exec", perm: sdk.PermExecute, connCh: ch}
	sentinel := errors.New("open failed")
	eng := newResolveEngine(t, h, &stubResolver{err: sentinel})

	_, err := eng.Run(context.Background(), command.Request{
		PluginID: "demo", CommandID: "exec", TargetID: "srv01",
	})
	var se *sdk.Error
	if !errors.As(err, &se) || se.Code != "CONNECTION_OPEN_FAILED" {
		t.Errorf("want CONNECTION_OPEN_FAILED, got %v", err)
	}
}
