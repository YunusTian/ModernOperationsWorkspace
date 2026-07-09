package plugin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// fake plugin
// -----------------------------------------------------------------------------

type fakePlugin struct {
	id       string
	initErr  error
	cmds     []sdk.CommandHandler
	initHits int
	downHits int
}

func (p *fakePlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: p.id, Name: p.id, Version: "0.0.1"}
}
func (p *fakePlugin) Init(ctx context.Context, r sdk.InitRequest) error {
	p.initHits++
	return p.initErr
}
func (p *fakePlugin) Shutdown(ctx context.Context) error { p.downHits++; return nil }
func (p *fakePlugin) HealthCheck(ctx context.Context) sdk.HealthStatus {
	return sdk.StatusHealthy
}
func (p *fakePlugin) Commands() []sdk.CommandHandler { return p.cmds }

type fakeCmd struct{ id string }

func (c *fakeCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{ID: c.id, Permission: sdk.PermRead}
}
func (c *fakeCmd) Execute(ctx context.Context, r *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	return &sdk.ExecuteResponse{}, nil
}
func (c *fakeCmd) ExecuteStream(ctx context.Context, s sdk.Stream) error { return sdk.ErrNotSupported }

// -----------------------------------------------------------------------------
// tests
// -----------------------------------------------------------------------------

func TestRegisterAndLifecycle(t *testing.T) {
	m := plugin.NewManager(plugin.Options{})
	p := &fakePlugin{id: "ssh", cmds: []sdk.CommandHandler{&fakeCmd{id: "exec"}}}

	if err := m.Register(p); err != nil {
		t.Fatalf("register: %v", err)
	}
	entry, ok := m.Get("ssh")
	if !ok || entry.State != plugin.StateRegistered {
		t.Fatalf("want StateRegistered, got %+v", entry)
	}

	ctx := context.Background()
	if err := m.Enable(ctx, "ssh", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if p.initHits != 1 {
		t.Errorf("Init should be called once, got %d", p.initHits)
	}
	entry, _ = m.Get("ssh")
	if entry.State != plugin.StateEnabled {
		t.Fatalf("want StateEnabled, got %s", entry.State)
	}

	// Enable 幂等
	_ = m.Enable(ctx, "ssh", sdk.InitRequest{})
	if p.initHits != 1 {
		t.Errorf("Init should stay 1 after re-enable, got %d", p.initHits)
	}

	// 找到 Command
	h, err := m.Command("ssh", "exec")
	if err != nil || h == nil {
		t.Fatalf("Command lookup failed: %v", err)
	}

	// Disable
	if err := m.Disable(ctx, "ssh"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if p.downHits != 1 {
		t.Errorf("Shutdown should be called once, got %d", p.downHits)
	}

	// Disabled 后 Command 拿不到
	if _, err := m.Command("ssh", "exec"); err == nil {
		t.Error("Command should fail when plugin disabled")
	}
}

func TestRegisterDuplicate(t *testing.T) {
	m := plugin.NewManager(plugin.Options{})
	p := &fakePlugin{id: "ssh"}
	_ = m.Register(p)
	if err := m.Register(p); err == nil {
		t.Error("second register should fail")
	}
}

func TestEnableInitFailure(t *testing.T) {
	m := plugin.NewManager(plugin.Options{})
	sentinel := errors.New("boom")
	p := &fakePlugin{id: "docker", initErr: sentinel}
	_ = m.Register(p)

	err := m.Enable(context.Background(), "docker", sdk.InitRequest{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel, got %v", err)
	}
	e, _ := m.Get("docker")
	if e.State != plugin.StateFailed {
		t.Errorf("want StateFailed, got %s", e.State)
	}
}

func TestCommandNotFound(t *testing.T) {
	m := plugin.NewManager(plugin.Options{})
	if _, err := m.Command("nope", "x"); !errors.Is(err, plugin.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestList(t *testing.T) {
	m := plugin.NewManager(plugin.Options{})
	_ = m.Register(&fakePlugin{id: "b"})
	_ = m.Register(&fakePlugin{id: "a"})
	ids := m.List()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("List should be sorted, got %v", ids)
	}
}
