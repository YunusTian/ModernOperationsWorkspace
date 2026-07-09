package recipe_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/core/recipe"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// 静态注册 / builtin 校验
// -----------------------------------------------------------------------------

func TestRegistry_Builtins(t *testing.T) {
	reg := recipe.NewRegistry()
	for _, id := range []string{"system.cpu", "system.disk"} {
		rp, ok := reg.Get(id)
		if !ok {
			t.Fatalf("builtin recipe %q missing", id)
		}
		if err := rp.Validate(); err != nil {
			t.Errorf("recipe %s invalid: %v", id, err)
		}
		if len(rp.Steps) == 0 {
			t.Errorf("recipe %s should have steps", id)
		}
	}
}

func TestRegistry_RejectDuplicate(t *testing.T) {
	reg := recipe.NewRegistry()
	err := reg.Register(&recipe.Recipe{
		ID:    "system.cpu",
		Steps: []recipe.Step{{ID: "x", Plugin: "ssh", Command: "exec"}},
	})
	if err == nil {
		t.Error("duplicate id should fail")
	}
}

// -----------------------------------------------------------------------------
// Runner 行为：单元层用假 plugin 模拟 Command
// -----------------------------------------------------------------------------

// echoCmd：把 params 原样返回；support/fail 由 spec 切换。
type echoCmd struct {
	id   string
	fail bool
}

func (c *echoCmd) Spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		ID: c.id, Permission: sdk.PermRead,
	}
}
func (c *echoCmd) Execute(ctx context.Context, r *sdk.ExecuteRequest) (*sdk.ExecuteResponse, error) {
	if c.fail {
		return nil, sdk.NewError("BOOM", "explode", nil)
	}
	return &sdk.ExecuteResponse{Data: r.Params}, nil
}
func (c *echoCmd) ExecuteStream(context.Context, sdk.Stream) error {
	return sdk.ErrNotSupported
}

type fakePlugin struct {
	id   string
	cmds []sdk.CommandHandler
}

func (p *fakePlugin) Metadata() sdk.Metadata {
	return sdk.Metadata{ID: p.id, Name: p.id, Version: "0.0.1"}
}
func (p *fakePlugin) Init(context.Context, sdk.InitRequest) error {
	return nil
}
func (p *fakePlugin) Shutdown(context.Context) error               { return nil }
func (p *fakePlugin) HealthCheck(context.Context) sdk.HealthStatus { return sdk.StatusHealthy }
func (p *fakePlugin) Commands() []sdk.CommandHandler               { return p.cmds }

func newEngine(t *testing.T, cmds ...sdk.CommandHandler) *command.Engine {
	t.Helper()
	pm := plugin.NewManager(plugin.Options{})
	if err := pm.Register(&fakePlugin{id: "fake", cmds: cmds}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := pm.Enable(context.Background(), "fake", sdk.InitRequest{}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	return command.New(command.Options{Manager: pm})
}

func TestRunner_SuccessSequential(t *testing.T) {
	eng := newEngine(t, &echoCmd{id: "a"}, &echoCmd{id: "b"})
	runner := recipe.NewRunner(eng)

	rp := &recipe.Recipe{
		ID: "test",
		Steps: []recipe.Step{
			{ID: "s1", Plugin: "fake", Command: "a", Params: map[string]any{"x": 1}},
			{ID: "s2", Plugin: "fake", Command: "b"},
		},
	}
	res, err := runner.Run(context.Background(), rp, recipe.RunOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.OK {
		t.Fatalf("res.OK want true")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(res.Steps))
	}
	for _, s := range res.Steps {
		if !s.OK {
			t.Errorf("step %s should be OK", s.StepID)
		}
	}
	// s1 应把 params 原样带回
	var got map[string]any
	if err := json.Unmarshal(res.Steps[0].Data, &got); err != nil {
		t.Fatalf("unmarshal step data: %v", err)
	}
	if got["x"].(float64) != 1 {
		t.Errorf("step data mismatch: %v", got)
	}
}

func TestRunner_StopsOnFailure(t *testing.T) {
	eng := newEngine(t, &echoCmd{id: "a"}, &echoCmd{id: "b", fail: true}, &echoCmd{id: "c"})
	runner := recipe.NewRunner(eng)

	rp := &recipe.Recipe{
		ID: "test",
		Steps: []recipe.Step{
			{ID: "s1", Plugin: "fake", Command: "a"},
			{ID: "s2", Plugin: "fake", Command: "b"},
			{ID: "s3", Plugin: "fake", Command: "c"},
		},
	}
	res, err := runner.Run(context.Background(), rp, recipe.RunOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if res.OK {
		t.Error("res.OK should be false")
	}
	if len(res.Steps) != 2 {
		t.Fatalf("should stop at step 2, got %d steps", len(res.Steps))
	}
	if res.Steps[1].ErrorCode != "BOOM" {
		t.Errorf("step[1] code want BOOM got %q", res.Steps[1].ErrorCode)
	}
	// 未执行的 s3 不应出现
	for _, s := range res.Steps {
		if s.StepID == "s3" {
			t.Error("s3 should not have executed")
		}
	}
}

func TestRunner_ValidateEmptyRecipe(t *testing.T) {
	eng := newEngine(t, &echoCmd{id: "a"})
	runner := recipe.NewRunner(eng)
	_, err := runner.Run(context.Background(), &recipe.Recipe{ID: "empty"}, recipe.RunOptions{})
	if err == nil {
		t.Error("empty recipe should fail")
	}
}

var _ = errors.New // silence unused if branch cleaned
