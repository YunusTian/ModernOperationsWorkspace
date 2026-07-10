// Workflow e2e：跑一遍 examples/workflows/deploy-static-site.yaml 的最小子集。
//
// 目的：验证 Workflow Runner ↔ Command Engine ↔ SSH Plugin ↔ 真实 SSH 服务器链路，
// 以及 ${inputs.*} 插值在最终发到 fake server 的命令行中被正确替换。
//
// 复用 helpers_test.go 里的 fake SSH server 与 rig：
//   - fake server 的 echoHandler 会把收到的 cmdline 回显到 stdout
//   - 我们通过 observe channel 抓每一步实际发到 server 的命令
//   - 断言插值命中且步骤按顺序执行

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/recipe"
	"github.com/mow/mow/core/workflow"
	"github.com/mow/mow/sdk"
)

// engineCmdExecutor 把 workflow.CommandExecutor 桥接到测试 rig 里的 command.Engine。
// 与 apps/cli/workflow.go 的 Adapter 语义一致，独立复制一份避免跨 module import。
type engineCmdExecutor struct{ eng *command.Engine }

func (a *engineCmdExecutor) RunCommand(
	ctx context.Context, cmdID string,
	params map[string]any, opts workflow.CommandRunOptions,
) (*workflow.StepOutput, error) {
	i := strings.IndexByte(cmdID, '.')
	if i <= 0 || i == len(cmdID)-1 {
		return nil, fmt.Errorf("invalid command id: %q", cmdID)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	caller, _ := opts.Caller.(sdk.Caller)
	resp, err := a.eng.Run(ctx, command.Request{
		PluginID:  cmdID[:i],
		CommandID: cmdID[i+1:],
		Params:    raw,
		TargetID:  opts.TargetID,
		Timeout:   opts.Timeout,
		Caller:    caller,
	})
	if err != nil {
		return nil, err
	}
	return &workflow.StepOutput{AuditID: resp.AuditID, Data: resp.Data}, nil
}

type registryRecipeExecutor struct {
	runner   *recipe.Runner
	registry *recipe.Registry
}

func (a *registryRecipeExecutor) RunRecipe(
	ctx context.Context, id string,
	_ map[string]any, opts workflow.CommandRunOptions,
) (*workflow.StepOutput, error) {
	rp, ok := a.registry.Get(id)
	if !ok {
		return nil, fmt.Errorf("recipe not found: %s", id)
	}
	caller, _ := opts.Caller.(sdk.Caller)
	res, err := a.runner.Run(ctx, rp, recipe.RunOptions{
		TargetID: opts.TargetID,
		Caller:   caller,
	})
	if err != nil {
		return nil, err
	}
	data, _ := json.Marshal(res)
	return &workflow.StepOutput{Data: data}, nil
}

func TestWorkflow_DeployStaticSite_EndToEnd(t *testing.T) {
	const user, password = "u", "p"

	// 观察 fake server 收到的命令行；容量足够容纳三步。
	observed := make(chan string, 8)
	fs := startFakeSSHServer(t, echoHandler(0, observed), withPassword(user, password))

	r := newRig(t)
	r.upsertPasswordTarget(t, "srv1", "127.0.0.1", fs.Port, user, password)

	// 加载 example workflow（YAML）
	wfPath, err := filepath.Abs(filepath.Join("..", "..", "examples", "workflows", "deploy-static-site.yaml"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	wf, err := workflow.LoadFile(wfPath)
	if err != nil {
		t.Fatalf("load workflow: %v", err)
	}
	if wf.ID != "deploy.static-site" {
		t.Fatalf("unexpected workflow id: %s", wf.ID)
	}

	// 构造 Runner
	runner := workflow.NewRunner(workflow.RunnerOptions{
		Command: &engineCmdExecutor{eng: r.Engine},
		Recipe:  &registryRecipeExecutor{runner: r.Runner, registry: r.Recipes},
	})

	// 收集 OnStep 事件
	var startedIDs []string
	events := 0
	onStep := func(ev workflow.StepEvent) {
		events++
		if ev.Phase == workflow.PhaseStart {
			startedIDs = append(startedIDs, ev.Step.ID)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := runner.Run(ctx, wf, workflow.RunOptions{
		Inputs: map[string]any{
			"site":        "hello",
			"local_dir":   "/tmp/dist",
			"remote_dir":  "/var/www/hello",
			"health_port": 8080,
		},
		TargetID: "srv1",
		Caller:   sdk.Caller{Type: sdk.CallerCLI, User: "e2e"},
		OnStep:   onStep,
	})

	// -----------------------------------------------------------------------
	// 结构断言
	// -----------------------------------------------------------------------
	if err != nil {
		t.Fatalf("workflow run: %v", err)
	}
	if !res.OK {
		t.Fatalf("workflow not OK: %+v", res.Steps)
	}
	if len(res.Steps) != 3 {
		t.Fatalf("expected 3 step results, got %d", len(res.Steps))
	}
	wantOrder := []string{"backup", "upload", "health"}
	for i, want := range wantOrder {
		if res.Steps[i].StepID != want {
			t.Errorf("step[%d].id = %s, want %s", i, res.Steps[i].StepID, want)
		}
		if !res.Steps[i].OK {
			t.Errorf("step %s not OK: %+v", want, res.Steps[i])
		}
	}
	// OnStep：3 步 × 2 阶段（start + finish）= 6 次回调
	if events != 6 {
		t.Errorf("OnStep events = %d, want 6", events)
	}
	if strings.Join(startedIDs, ",") != "backup,upload,health" {
		t.Errorf("start order = %v", startedIDs)
	}

	// -----------------------------------------------------------------------
	// 插值断言：fake server 收到的每条命令行必须已经不含 ${...}
	// 且包含预期 inputs 值
	// -----------------------------------------------------------------------
	close(observed)
	var cmds []string
	for c := range observed {
		cmds = append(cmds, c)
	}
	if len(cmds) != 3 {
		t.Fatalf("fake server saw %d commands, want 3: %v", len(cmds), cmds)
	}
	for i, c := range cmds {
		if strings.Contains(c, "${") {
			t.Errorf("cmd[%d] still contains unresolved placeholder: %q", i, c)
		}
	}
	if !strings.Contains(cmds[0], "/var/www/hello") || !strings.Contains(cmds[0], "hello") {
		t.Errorf("backup cmd = %q", cmds[0])
	}
	if !strings.Contains(cmds[1], "uploaded hello from /tmp/dist to /var/www/hello") {
		t.Errorf("upload cmd = %q", cmds[1])
	}
	if !strings.Contains(cmds[2], ":8080 ") {
		t.Errorf("health cmd = %q", cmds[2])
	}

	// -----------------------------------------------------------------------
	// 输出断言：每步 stdout 应包含 fake server 的 echo: 前缀
	// -----------------------------------------------------------------------
	var out execResult
	if err := json.Unmarshal(res.Steps[0].Data, &out); err != nil {
		t.Fatalf("unmarshal step[0] data: %v", err)
	}
	if !strings.HasPrefix(out.Stdout, "echo:") {
		t.Errorf("step[0].stdout = %q", out.Stdout)
	}
	if out.ExitCode != 0 {
		t.Errorf("step[0].exit_code = %d", out.ExitCode)
	}
}
