// Recipe e2e：用内置 system.cpu 通过 fake SSH server 跑一遍，验证 Runner ↔ Engine ↔ Plugin 链路。
package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mow/mow/core/recipe"
	"github.com/mow/mow/sdk"
)

func TestRecipe_SystemCPU_EndToEnd(t *testing.T) {
	const user, password = "u", "p"
	fs := startFakeSSHServer(t, echoHandler(0, nil), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv1", "127.0.0.1", fs.Port, user, password)

	rp, ok := r.Recipes.Get("system.cpu")
	if !ok {
		t.Fatalf("builtin recipe system.cpu missing")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := r.Runner.Run(ctx, rp, recipe.RunOptions{
		TargetID: "srv1",
		Caller:   sdk.Caller{Type: sdk.CallerCLI, User: "test"},
	})
	if err != nil {
		t.Fatalf("recipe run: %v", err)
	}
	if !res.OK {
		t.Fatalf("recipe not OK: %+v", res.Steps)
	}
	if len(res.Steps) != len(rp.Steps) {
		t.Fatalf("expected %d step results, got %d", len(rp.Steps), len(res.Steps))
	}
	// 校验第一步的输出是 fake server 的 echo。
	var step0 execResult
	if err := json.Unmarshal(res.Steps[0].Data, &step0); err != nil {
		t.Fatalf("unmarshal step[0] data: %v", err)
	}
	want := "echo:cat /proc/loadavg\n"
	if step0.Stdout != want {
		t.Errorf("step[0].stdout want %q got %q", want, step0.Stdout)
	}
}
