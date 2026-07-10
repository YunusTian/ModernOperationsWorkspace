// workflow.go 桌面客户端的 Workflow 能力。
//
// 前端约定：
//   WorkflowValidate(yamlText string) → {ok, id, inputs[]}
//   WorkflowRun(sessionID, yamlText, inputs) → 立即返回
//     事件流：
//       workflow:<sessionID>:step   {phase, index, step_id, kind, ref, duration_ms, error_code?, error_msg?}
//       workflow:<sessionID>:done   {ok, duration_ms, error?}
//
// 依赖：底层 command.Engine + recipe.Runner；Adapter 与 apps/cli/workflow.go
// 结构一致，独立复制一份避免跨 module 依赖。

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/recipe"
	"github.com/mow/mow/core/workflow"
	"github.com/mow/mow/core/workflow/history"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// 前端可见的视图模型
// -----------------------------------------------------------------------------

// WorkflowInputVM 是 Workflow.Input 的前端投影。
type WorkflowInputVM struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Default     any    `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
}

// WorkflowValidateResult 是 WorkflowValidate 的返回结构。
type WorkflowValidateResult struct {
	OK          bool              `json:"ok"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	StepCount   int               `json:"step_count"`
	Inputs      []WorkflowInputVM `json:"inputs"`
}

// WorkflowRecipesRegistry 与 CLI 复用同一个静态注册表；桌面客户端也共享。
// 惰性构造，避免在 App 上再增字段。
func (a *App) workflowRecipes() *recipe.Registry {
	a.wfMu.Lock()
	defer a.wfMu.Unlock()
	if a.wfReg == nil {
		a.wfReg = recipe.NewRegistry()
	}
	return a.wfReg
}

// -----------------------------------------------------------------------------
// WorkflowValidate
// -----------------------------------------------------------------------------

// WorkflowValidate 解析 + 校验 YAML；不执行任何步骤。
func (a *App) WorkflowValidate(yamlText string) (*WorkflowValidateResult, error) {
	wf, err := workflow.LoadBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	res := &WorkflowValidateResult{
		OK:          true,
		ID:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		StepCount:   len(wf.Steps),
	}
	for _, in := range wf.Inputs {
		res.Inputs = append(res.Inputs, WorkflowInputVM{
			Name:        in.Name,
			Type:        string(in.Type),
			Required:    in.Required,
			Default:     in.Default,
			Description: in.Description,
		})
	}
	return res, nil
}

// -----------------------------------------------------------------------------
// WorkflowRun
// -----------------------------------------------------------------------------

// WorkflowRunInput 是前端调用 WorkflowRun 的入参。
type WorkflowRunInput struct {
	// SessionID：由前端生成的会话 id，用于事件频道命名。
	SessionID string `json:"session_id"`
	// YAML：Workflow YAML 文本。
	YAML string `json:"yaml"`
	// Target：默认 target id（可空，具体是否必需取决于 Workflow）。
	Target string `json:"target"`
	// Inputs：${inputs.*} 的实际取值。
	Inputs map[string]any `json:"inputs"`
}

// WorkflowRun 启动一个 Workflow 执行任务；立即返回，事件通过 wails EventsEmit 推送。
func (a *App) WorkflowRun(in WorkflowRunInput) error {
	if in.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	wf, err := workflow.LoadBytes([]byte(in.YAML))
	if err != nil {
		return err
	}

	// 提前把需要的插件 Enable
	rootCtx := a.wailsCtx()
	if err := a.ensureWorkflowPlugins(rootCtx, wf); err != nil {
		return err
	}

	runner := workflow.NewRunner(workflow.RunnerOptions{
		Command: &desktopCmdExecutor{eng: a.engine},
		Recipe: &desktopRecipeExecutor{
			runner:   recipe.NewRunner(a.engine),
			registry: a.workflowRecipes(),
		},
		History: a.workflowHistorySink(),
	})

	sess := in.SessionID
	emitCtx := a.wailsCtx()

	go func() {
		ctx, cancel := context.WithCancel(rootCtx)
		defer cancel()

		start := time.Now()
		res, runErr := runner.Run(ctx, wf, workflow.RunOptions{
			Inputs:      in.Inputs,
			TargetID:    in.Target,
			Caller:      sdk.Caller{Type: sdk.CallerDesktop, User: currentUser()},
			CallerLabel: "desktop:" + currentUser(),
			OnStep: func(ev workflow.StepEvent) {
				emitStepEvent(emitCtx, sess, ev)
			},
		})
		payload := map[string]any{
			"ok":          res != nil && res.OK,
			"duration_ms": time.Since(start).Milliseconds(),
		}
		if res != nil {
			payload["run_id"] = res.RunID
		}
		if runErr != nil {
			payload["error"] = runErr.Error()
		}
		wailsruntime.EventsEmit(emitCtx, "workflow:"+sess+":done", payload)
	}()

	return nil
}

func emitStepEvent(ctx context.Context, sess string, ev workflow.StepEvent) {
	kind := "cmd"
	ref := ev.Step.Command
	if ev.Step.Recipe != "" {
		kind = "recipe"
		ref = ev.Step.Recipe
	}
	payload := map[string]any{
		"phase":   string(ev.Phase),
		"index":   ev.Index,
		"step_id": ev.Step.ID,
		"kind":    kind,
		"ref":     ref,
	}
	if ev.Step.When != "" {
		payload["when"] = ev.Step.When
	}
	if ev.Phase == workflow.PhaseRetry {
		payload["attempt"] = ev.Attempt
		payload["max_attempts"] = ev.MaxAttempts
		payload["next_backoff_ms"] = ev.NextBackoff.Milliseconds()
		if ev.Err != nil {
			payload["error_msg"] = ev.Err.Error()
		}
	}
	if ev.Result != nil {
		payload["duration_ms"] = ev.Result.Duration.Milliseconds()
		if ev.Result.Skipped {
			payload["skipped"] = true
		}
		if ev.Result.Attempts > 0 {
			payload["attempts"] = ev.Result.Attempts
		}
		if ev.Result.ErrorCode != "" {
			payload["error_code"] = ev.Result.ErrorCode
		}
		if ev.Result.ErrorMsg != "" && ev.Phase != workflow.PhaseRetry {
			payload["error_msg"] = ev.Result.ErrorMsg
		}
	}
	wailsruntime.EventsEmit(ctx, "workflow:"+sess+":step", payload)
}

// -----------------------------------------------------------------------------
// 依赖插件自动 Enable
// -----------------------------------------------------------------------------

func (a *App) ensureWorkflowPlugins(ctx context.Context, wf *workflow.Workflow) error {
	seen := map[string]struct{}{}
	add := func(pluginID string) error {
		if pluginID == "" {
			return nil
		}
		if _, ok := seen[pluginID]; ok {
			return nil
		}
		seen[pluginID] = struct{}{}
		return a.ensurePlugin(ctx, pluginID)
	}
	for _, s := range wf.Steps {
		switch {
		case s.Command != "":
			i := strings.IndexByte(s.Command, '.')
			if i <= 0 || i == len(s.Command)-1 {
				return fmt.Errorf("step %q: invalid command %q", s.ID, s.Command)
			}
			if err := add(s.Command[:i]); err != nil {
				return err
			}
		case s.Recipe != "":
			rp, ok := a.workflowRecipes().Get(s.Recipe)
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
// Adapter：workflow.CommandExecutor / RecipeExecutor
// -----------------------------------------------------------------------------

type desktopCmdExecutor struct{ eng *command.Engine }

func (a *desktopCmdExecutor) RunCommand(
	ctx context.Context, cmdID string,
	params map[string]any, opts workflow.CommandRunOptions,
) (*workflow.StepOutput, error) {
	i := strings.IndexByte(cmdID, '.')
	if i <= 0 || i == len(cmdID)-1 {
		return nil, fmt.Errorf("invalid command %q", cmdID)
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

type desktopRecipeExecutor struct {
	runner   *recipe.Runner
	registry *recipe.Registry
}

func (a *desktopRecipeExecutor) RunRecipe(
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

// -----------------------------------------------------------------------------
// Workflow 执行历史
// -----------------------------------------------------------------------------
//
// 前端契约：
//   ListWorkflowRuns({limit, workflow_id}) → WorkflowRunRow[]
//   GetWorkflowRun(runID)                 → WorkflowRunDetail
//
// 与 CLI 共享同一份 JSONL 存储：apps/desktop 用 openHistory 打开、apps/cli 用 mustHistoryStore。

// WorkflowRunRow 是历史列表页每一行；不含 Steps / Inputs 明细。
type WorkflowRunRow struct {
	RunID       string `json:"run_id"`
	WorkflowID  string `json:"workflow_id"`
	TargetID    string `json:"target_id,omitempty"`
	Caller      string `json:"caller,omitempty"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	StartedAt   string `json:"started_at"`  // RFC3339
	FinishedAt  string `json:"finished_at"` // RFC3339
	DurationMs  int64  `json:"duration_ms"`
	StepCount   int    `json:"step_count"`
	SkippedCnt  int    `json:"skipped_count,omitempty"`
	RetriedCnt  int    `json:"retried_count,omitempty"`
	FailedStep  string `json:"failed_step,omitempty"`
}

// WorkflowRunDetail 是详情页需要的完整快照。
type WorkflowRunDetail struct {
	Row    WorkflowRunRow         `json:"row"`
	Inputs map[string]any         `json:"inputs,omitempty"`
	Steps  []WorkflowRunStepView  `json:"steps,omitempty"`
}

// WorkflowRunStepView 与 core/workflow.StepResult 对齐；避免前端解引用 StepResult 里的
// json.RawMessage（Wails 序列化二次编码问题）。
type WorkflowRunStepView struct {
	StepID     string `json:"step_id"`
	Command    string `json:"command,omitempty"`
	Recipe     string `json:"recipe,omitempty"`
	OK         bool   `json:"ok"`
	Skipped    bool   `json:"skipped,omitempty"`
	AuditID    string `json:"audit_id,omitempty"`
	Attempts   int    `json:"attempts,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	ErrorCode  string `json:"error_code,omitempty"`
	ErrorMsg   string `json:"error_msg,omitempty"`
}

// WorkflowHistoryListInput 是 ListWorkflowRuns 的入参。
type WorkflowHistoryListInput struct {
	Limit      int    `json:"limit"`
	WorkflowID string `json:"workflow_id,omitempty"`
}

// workflowHistorySink 是给 Runner 的 HistorySink；未启用时返回 nil。
func (a *App) workflowHistorySink() workflow.HistorySink {
	if a.history == nil {
		return nil
	}
	return a.history
}

// ListWorkflowRuns 返回最近的 Workflow 执行记录（新在前）。
func (a *App) ListWorkflowRuns(in WorkflowHistoryListInput) ([]WorkflowRunRow, error) {
	if a.history == nil {
		return nil, fmt.Errorf("workflow history is disabled")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 30
	}
	list, err := a.history.List(a.wailsCtx(), history.ListOptions{
		Limit: limit, WorkflowID: in.WorkflowID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowRunRow, 0, len(list))
	for _, r := range list {
		out = append(out, toRunRow(r))
	}
	return out, nil
}

// GetWorkflowRun 返回单条执行详情；找不到时返回错误。
func (a *App) GetWorkflowRun(runID string) (*WorkflowRunDetail, error) {
	if a.history == nil {
		return nil, fmt.Errorf("workflow history is disabled")
	}
	if strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	rec, err := a.history.Get(a.wailsCtx(), runID)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("no such run: %s", runID)
	}
	detail := &WorkflowRunDetail{
		Row:    toRunRow(*rec),
		Inputs: rec.Inputs,
	}
	for _, s := range rec.Steps {
		detail.Steps = append(detail.Steps, WorkflowRunStepView{
			StepID:     s.StepID,
			Command:    s.Command,
			Recipe:     s.Recipe,
			OK:         s.OK,
			Skipped:    s.Skipped,
			AuditID:    s.AuditID,
			Attempts:   s.Attempts,
			DurationMs: s.Duration.Milliseconds(),
			ErrorCode:  s.ErrorCode,
			ErrorMsg:   s.ErrorMsg,
		})
	}
	return detail, nil
}

func toRunRow(r history.Record) WorkflowRunRow {
	row := WorkflowRunRow{
		RunID:      r.RunID,
		WorkflowID: r.WorkflowID,
		TargetID:   r.TargetID,
		Caller:     r.Caller,
		OK:         r.OK,
		Error:      r.Error,
		StartedAt:  r.StartedAt.UTC().Format(time.RFC3339),
		FinishedAt: r.FinishedAt.UTC().Format(time.RFC3339),
		DurationMs: r.Duration.Milliseconds(),
		StepCount:  len(r.Steps),
	}
	for _, s := range r.Steps {
		if s.Skipped {
			row.SkippedCnt++
		}
		if s.Attempts > 1 {
			row.RetriedCnt++
		}
		if !s.OK && !s.Skipped && row.FailedStep == "" {
			row.FailedStep = s.StepID
		}
	}
	return row
}
