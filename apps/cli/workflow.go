package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/recipe"
	"github.com/mow/mow/core/workflow"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// mow workflow validate|run <file.yaml>
// -----------------------------------------------------------------------------

func newWorkflowCmd(h *appHolder) *cobra.Command {
	c := &cobra.Command{
		Use:   "workflow",
		Short: "Validate and run Workflows (YAML DSL)",
	}
	c.AddCommand(
		newWorkflowValidateCmd(),
		newWorkflowRunCmd(h),
	)
	return c
}

// -----------------------------------------------------------------------------
// mow workflow validate <file>
// -----------------------------------------------------------------------------

func newWorkflowValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <file.yaml>",
		Short: "Load and validate a Workflow YAML without executing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := workflow.LoadFile(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok: %s (%d steps)\n", w.ID, len(w.Steps))
			return nil
		},
	}
}

// -----------------------------------------------------------------------------
// mow workflow run <file> --target=X --input k=v
// -----------------------------------------------------------------------------

type workflowRunOpts struct {
	Target     string
	Inputs     []string // k=v，来自 --input
	InputsJSON string   // 完整 JSON（--inputs-json）
	AsJSON     bool
	NoColor    bool
}

func newWorkflowRunCmd(h *appHolder) *cobra.Command {
	o := &workflowRunOpts{}
	c := &cobra.Command{
		Use:   "run <file.yaml>",
		Short: "Execute a Workflow YAML",
		Long: `Execute a Workflow YAML file through Command Engine + Recipe Runner.

Inputs (later overrides earlier):
  --input key=value      repeatable
  --inputs-json '{...}'  full JSON body`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflow(cmd.Context(), h, o, args[0])
		},
	}
	f := c.Flags()
	f.StringVar(&o.Target, "target", "", "default target ID for all steps")
	f.StringSliceVar(&o.Inputs, "input", nil, "workflow input in key=value form (repeatable)")
	f.StringVar(&o.InputsJSON, "inputs-json", "", "workflow inputs as JSON object")
	f.BoolVar(&o.AsJSON, "json", false, "print final Result as JSON (suppress progress)")
	f.BoolVar(&o.NoColor, "no-color", false, "disable ANSI colors")
	return c
}

func runWorkflow(ctx context.Context, h *appHolder, o *workflowRunOpts, path string) error {
	w, err := workflow.LoadFile(path)
	if err != nil {
		return err
	}

	app, err := h.Load()
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer app.Close(ctx)

	// 提前 Enable Workflow 中所有 Command / Recipe 依赖的插件。
	if err := ensureWorkflowPlugins(ctx, app, w); err != nil {
		return err
	}

	inputs, err := buildWorkflowInputs(o, w)
	if err != nil {
		return err
	}

	runner := workflow.NewRunner(workflow.RunnerOptions{
		Command: &engineCommandExecutor{engine: app.Engine},
		Recipe:  &registryRecipeExecutor{runner: app.Runner, registry: app.Recipes},
	})

	var onStep workflow.OnStepFunc
	writer := io.Writer(os.Stdout)
	if !o.AsJSON {
		onStep = newProgressPrinter(writer, filepath.Base(path), useColor(o))
	}

	res, runErr := runner.Run(ctx, w, workflow.RunOptions{
		Inputs:   inputs,
		TargetID: o.Target,
		Caller:   sdk.Caller{Type: sdk.CallerCLI, User: currentUser()},
		OnStep:   onStep,
	})

	if o.AsJSON {
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		printWorkflowSummary(writer, res, useColor(o))
	}
	return runErr
}

// -----------------------------------------------------------------------------
// Adapter：workflow.CommandExecutor / RecipeExecutor
// -----------------------------------------------------------------------------

// engineCommandExecutor 把 workflow.CommandExecutor 桥接到 core/command.Engine。
type engineCommandExecutor struct {
	engine *command.Engine
}

func (a *engineCommandExecutor) RunCommand(
	ctx context.Context, cmdID string,
	params map[string]any, opts workflow.CommandRunOptions,
) (*workflow.StepOutput, error) {
	pluginID, cid, err := splitFQID(cmdID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	caller, _ := opts.Caller.(sdk.Caller)

	resp, err := a.engine.Run(ctx, command.Request{
		PluginID:  pluginID,
		CommandID: cid,
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

// registryRecipeExecutor 把 workflow.RecipeExecutor 桥接到 core/recipe.Runner。
//
// recipe.Runner 需要完整的 *Recipe 实例，因此这里通过 Registry 按 ID 查表。
// 注意：v0.2 内置 Recipe 不接受 params（Steps 静态定义），若 workflow.Step
// 传入了 params，这里会被静默丢弃 —— 与当前 recipe.Runner 语义一致。
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
	data, _ := json.Marshal(res) // 让下游 ${steps.<id>.out.*} 能访问 recipe 结果
	return &workflow.StepOutput{Data: data}, nil
}

// -----------------------------------------------------------------------------
// 依赖插件自动 Enable
// -----------------------------------------------------------------------------

func ensureWorkflowPlugins(ctx context.Context, app *App, w *workflow.Workflow) error {
	seen := map[string]struct{}{}
	add := func(pluginID string) error {
		if pluginID == "" {
			return nil
		}
		if _, ok := seen[pluginID]; ok {
			return nil
		}
		seen[pluginID] = struct{}{}
		return app.ensurePluginEnabled(ctx, pluginID)
	}

	for _, s := range w.Steps {
		switch {
		case s.Command != "":
			pluginID, _, err := splitFQID(s.Command)
			if err != nil {
				return fmt.Errorf("step %q: %w", s.ID, err)
			}
			if err := add(pluginID); err != nil {
				return err
			}
		case s.Recipe != "":
			rp, ok := app.Recipes.Get(s.Recipe)
			if !ok {
				return fmt.Errorf("step %q: recipe not found: %s", s.ID, s.Recipe)
			}
			for _, rs := range rp.Steps {
				if err := add(rs.Plugin); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Inputs
// -----------------------------------------------------------------------------

func buildWorkflowInputs(o *workflowRunOpts, w *workflow.Workflow) (map[string]any, error) {
	m := map[string]any{}
	// 优先填默认值
	for _, in := range w.Inputs {
		if in.Default != nil {
			m[in.Name] = in.Default
		}
	}
	for _, kv := range o.Inputs {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, fmt.Errorf("invalid --input %q, expected key=value", kv)
		}
		m[parts[0]] = parts[1]
	}
	if o.InputsJSON != "" {
		var override map[string]any
		if err := json.Unmarshal([]byte(o.InputsJSON), &override); err != nil {
			return nil, fmt.Errorf("--inputs-json: %w", err)
		}
		for k, v := range override {
			m[k] = v
		}
	}
	// 校验：Required 且缺失
	for _, in := range w.Inputs {
		if in.Required {
			if _, ok := m[in.Name]; !ok {
				return nil, fmt.Errorf("missing required input: %s", in.Name)
			}
		}
	}
	return m, nil
}

// -----------------------------------------------------------------------------
// 彩色进度打印
// -----------------------------------------------------------------------------

const (
	ansiReset  = "\x1b[0m"
	ansiDim    = "\x1b[2m"
	ansiBold   = "\x1b[1m"
	ansiCyan   = "\x1b[36m"
	ansiGreen  = "\x1b[32m"
	ansiRed    = "\x1b[31m"
	ansiYellow = "\x1b[33m"
)

// useColor 决定是否输出 ANSI 转义。
// 关闭规则：--no-color、NO_COLOR 环境变量、非 TTY 均视为关闭。
func useColor(o *workflowRunOpts) bool {
	if o.NoColor {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	// 简单启发：非 TTY（如管道）不上色；此处放宽为始终启用，交给 --no-color 关闭
	return true
}

type progressPrinter struct {
	w      io.Writer
	file   string
	color  bool
	starts map[int]time.Time
}

func newProgressPrinter(w io.Writer, file string, color bool) workflow.OnStepFunc {
	p := &progressPrinter{w: w, file: file, color: color, starts: map[int]time.Time{}}
	fmt.Fprintf(w, "%sworkflow%s %s\n", p.c(ansiBold), p.c(ansiReset), file)
	return p.onStep
}

func (p *progressPrinter) c(code string) string {
	if p.color {
		return code
	}
	return ""
}

func (p *progressPrinter) onStep(ev workflow.StepEvent) {
	kind := ev.Step.Command
	kindTag := "cmd"
	if kind == "" {
		kind = ev.Step.Recipe
		kindTag = "recipe"
	}

	switch ev.Phase {
	case workflow.PhaseStart:
		p.starts[ev.Index] = time.Now()
		fmt.Fprintf(p.w, "%s▶%s %s%s%s %s(%s:%s)%s ... ",
			p.c(ansiCyan), p.c(ansiReset),
			p.c(ansiBold), ev.Step.ID, p.c(ansiReset),
			p.c(ansiDim), kindTag, kind, p.c(ansiReset),
		)

	case workflow.PhaseFinish:
		fmt.Fprintf(p.w, "%s✓%s %s\n",
			p.c(ansiGreen), p.c(ansiReset),
			formatDur(ev.Result.Duration),
		)

	case workflow.PhaseSkip:
		when := ""
		if ev.Step.When != "" {
			when = fmt.Sprintf(" %s(when=%s)%s", p.c(ansiDim), ev.Step.When, p.c(ansiReset))
		}
		fmt.Fprintf(p.w, "%s⤼%s skipped%s\n",
			p.c(ansiYellow), p.c(ansiReset), when,
		)

	case workflow.PhaseError:
		code := ""
		msg := ""
		if ev.Result != nil {
			code = ev.Result.ErrorCode
			msg = ev.Result.ErrorMsg
		}
		fmt.Fprintf(p.w, "%s✗%s %s %s[%s]%s %s\n",
			p.c(ansiRed), p.c(ansiReset),
			formatDur(ev.Result.Duration),
			p.c(ansiYellow), code, p.c(ansiReset),
			msg,
		)
	}
}

func formatDur(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(time.Millisecond).String()
}

func printWorkflowSummary(w io.Writer, res *workflow.Result, color bool) {
	if res == nil {
		return
	}
	status := "ok"
	col := ansiGreen
	if !res.OK {
		status = "FAILED"
		col = ansiRed
	}
	tag := status
	if color {
		tag = col + status + ansiReset
	}
	skipped := 0
	for _, s := range res.Steps {
		if s.Skipped {
			skipped++
		}
	}
	skippedTag := ""
	if skipped > 0 {
		skippedTag = fmt.Sprintf(" skipped=%d", skipped)
	}
	fmt.Fprintf(w, "\nworkflow=%s status=%s duration=%s%s\n",
		res.WorkflowID, tag, res.Duration.Round(time.Millisecond), skippedTag)
}
