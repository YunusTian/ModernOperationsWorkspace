// loader.go 负责将 YAML DSL 反序列化为 Workflow。
//
// v0.2 PR2：仅提供严格模式解析（未知字段报错），不涉及变量求值。
// 顶层结构参考 docs/workflow.md：
//
//	workflow:
//	  id: deploy.dotnet
//	  inputs: [...]
//	  steps:  [...]
//
// 解析后会调用 Workflow.Validate 做一次静态校验，避免调用方拿到半成品。
//
// v0.6.0 P0 追加字段（详见 docs/v0.6.0-design.md）：
//   - workflow.manifest_version / workflow.idempotency_key
//   - inputs[].schema  —— JSON Schema，复用 core/plugin/settings 编译器
//   - step.parallel_limit / step.target
//   - step.workflow  —— 子工作流调用，Loader 阶段就 resolve 相对路径 + 循环检测 + 深度限制
//   - step.parallel_group.branches[].{name,target,steps}
//
// 保持旧 API 兼容：LoadFile / LoadBytes / LoadReader 签名不变；
// 子工作流解析走内部 loaderContext（含路径栈、深度计数、baseDir）。

package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mow/mow/core/plugin/settings"
	"gopkg.in/yaml.v3"
)

// yamlDoc 是顶层文档结构：workflow: ...
type yamlDoc struct {
	Workflow *yamlWorkflow `yaml:"workflow"`
}

type yamlWorkflow struct {
	ID              string         `yaml:"id"`
	Name            string         `yaml:"name"`
	Description     string         `yaml:"description"`
	Inputs          []yamlInput    `yaml:"inputs"`
	Steps           []yamlStep     `yaml:"steps"`
	OnFailure       *yamlOnFailure `yaml:"on_failure"`
	IdempotencyKey  string         `yaml:"idempotency_key"`
	ManifestVersion int            `yaml:"manifest_version"`
}

// yamlOnFailure 是 workflow.on_failure 的 YAML 结构。
type yamlOnFailure struct {
	Rollback []string `yaml:"rollback"`
}

type yamlInput struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Required    bool   `yaml:"required"`
	Default     any    `yaml:"default"`
	Description string `yaml:"description"`
	// Schema 是 v0.6.0 新增字段：原始 JSON Schema map，编译后成 *settings.Schema。
	// 用 map[string]any 而非 yaml.Node，方便 json.Marshal 直接进 settings.Compile。
	Schema map[string]any `yaml:"schema"`
}

type yamlStep struct {
	ID         string          `yaml:"id"`
	Command    string          `yaml:"command"`
	Recipe     string          `yaml:"recipe"`
	Params     map[string]any  `yaml:"params"`
	Timeout    string          `yaml:"timeout"`
	When       string          `yaml:"when"`
	Retry      *yamlRetry      `yaml:"retry"`
	Compensate *yamlCompensate `yaml:"compensate"`
	Parallel   bool            `yaml:"parallel"`

	// v0.6.0 新增字段
	ParallelLimit int                `yaml:"parallel_limit"`
	Target        string             `yaml:"target"`
	Workflow      string             `yaml:"workflow"`       // 相对路径或 ref:// URI
	Inputs        map[string]any     `yaml:"inputs"`         // 传给子 workflow 的 inputs
	ParallelGroup *yamlParallelGroup `yaml:"parallel_group"` // 显式并行组
}

// yamlParallelGroup 是 step.parallel_group 的 YAML 结构（v0.6.0）。
type yamlParallelGroup struct {
	ParallelLimit int           `yaml:"parallel_limit"`
	Branches      []yamlBranch  `yaml:"branches"`
}

// yamlBranch 是 parallel_group.branches[] 的 YAML 结构。
type yamlBranch struct {
	Name   string     `yaml:"name"`
	Target string     `yaml:"target"`
	Steps  []yamlStep `yaml:"steps"`
}

// yamlCompensate 是 step.compensate 的 YAML 结构。
type yamlCompensate struct {
	Command string         `yaml:"command"`
	Recipe  string         `yaml:"recipe"`
	Params  map[string]any `yaml:"params"`
	Timeout string         `yaml:"timeout"`
}

// yamlRetry 是 retry: { ... } 的原始形态。所有字段均可选。
//
// 用字符串接 duration 是为了让 YAML 里写 "500ms" / "2s" 直观；数字则按 Max 处理。
type yamlRetry struct {
	Max         int    `yaml:"max"`
	Backoff     string `yaml:"backoff"`
	MaxBackoff  string `yaml:"max_backoff"`
	Exponential bool   `yaml:"exponential"`
}

// LoadFile 从文件路径加载并解析 Workflow。
//
// v0.6.0：内部走 loaderContext，可递归加载 step.workflow 子调用；
// baseDir 取 path 所在目录，作为相对子 workflow 的解析基准。
func LoadFile(path string) (*Workflow, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("workflow: abs %s: %w", path, err)
	}
	ctx := &loaderContext{
		stack: []string{abs},
		depth: 0,
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("workflow: open %s: %w", path, err)
	}
	defer f.Close()
	return loadReader(f, filepath.Dir(abs), ctx)
}

// LoadBytes 从字节切片解析 Workflow。子 workflow 场景下 relative-path 会因
// 缺少 baseDir 而报错（这是设计意图：字节流没有"目录"上下文）。
func LoadBytes(data []byte) (*Workflow, error) {
	return LoadReader(bytes.NewReader(data))
}

// LoadReader 从 io.Reader 解析 Workflow。
//
// 严格模式：任何未声明字段（拼写错误、遗留字段等）都会返回错误。
// baseDir 默认取当前工作目录（保持 v0.3 行为兼容）。
func LoadReader(r io.Reader) (*Workflow, error) {
	if r == nil {
		return nil, errors.New("workflow: reader is nil")
	}
	cwd, _ := os.Getwd()
	ctx := &loaderContext{
		stack: nil, // 无路径栈 → 子 workflow 循环检测降级为深度限制
		depth: 0,
	}
	return loadReader(r, cwd, ctx)
}

// loaderContext 记录一次递归解析过程中的公共状态，用于子 workflow 的路径栈 /
// 循环检测 / 深度限制。零值合法（表示顶层解析）。
type loaderContext struct {
	// stack 是从最外层到当前正在解析的 workflow 的**绝对路径**栈。
	// 检测循环时用 stack 中 canonical(abs) 是否已存在。
	stack []string
	// depth 是"当前 workflow 相对最外层的嵌套深度"（顶层=0）。
	// 超过 MaxSubWorkflowDepth → SUBWORKFLOW_DEPTH_EXCEEDED。
	depth int
}

// loadReader 是内部实现：接收 baseDir 用于相对子 workflow 路径解析。
func loadReader(r io.Reader, baseDir string, ctx *loaderContext) (*Workflow, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var doc yamlDoc
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("workflow: empty document")
		}
		return nil, fmt.Errorf("workflow: parse yaml: %w", err)
	}
	// 不允许多文档流：进一步读取应返回 EOF。
	var extra yamlDoc
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("workflow: multi-document yaml is not supported")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("workflow: parse yaml: %w", err)
	}

	if doc.Workflow == nil {
		return nil, errors.New("workflow: missing top-level 'workflow' key")
	}

	w, err := doc.Workflow.toWorkflow(baseDir, ctx)
	if err != nil {
		return nil, err
	}
	if err := w.Validate(); err != nil {
		return nil, fmt.Errorf("workflow: validate: %w", err)
	}
	return w, nil
}

func (y *yamlWorkflow) toWorkflow(baseDir string, ctx *loaderContext) (*Workflow, error) {
	w := &Workflow{
		ID:              y.ID,
		Name:            y.Name,
		Description:     y.Description,
		IdempotencyKey:  y.IdempotencyKey,
		ManifestVersion: y.ManifestVersion,
	}
	if len(y.Inputs) > 0 {
		w.Inputs = make([]Input, 0, len(y.Inputs))
		for _, yi := range y.Inputs {
			in := Input{
				Name:        yi.Name,
				Type:        InputType(yi.Type),
				Required:    yi.Required,
				Default:     yi.Default,
				Description: yi.Description,
			}
			if len(yi.Schema) > 0 {
				sch, err := compileInputSchema(yi.Schema)
				if err != nil {
					return nil, fmt.Errorf("workflow: input %q: %w", yi.Name, err)
				}
				in.Schema = sch
			}
			w.Inputs = append(w.Inputs, in)
		}
	}
	if len(y.Steps) > 0 {
		w.Steps = make([]Step, 0, len(y.Steps))
		for i, ys := range y.Steps {
			step, err := ys.toStep(fmt.Sprintf("step[%d]", i), baseDir, ctx)
			if err != nil {
				return nil, err
			}
			w.Steps = append(w.Steps, step)
		}
	}
	if y.OnFailure != nil {
		w.OnFailure = &FailurePolicy{Rollback: append([]string(nil), y.OnFailure.Rollback...)}
	}
	return w, nil
}

// toStep 把 yamlStep 转成 workflow.Step，负责 duration 解析、retry / compensate 拆包、
// 以及 v0.6.0 新增的 workflow 子调用递归加载与 parallel_group 展开。
//
// where 是错误信息里用于定位当前 step 的位置串，如 "step[3]" 或 "branch \"a\" step[0]"。
func (ys *yamlStep) toStep(where string, baseDir string, ctx *loaderContext) (Step, error) {
	step := Step{
		ID:                ys.ID,
		Command:           ys.Command,
		Recipe:            ys.Recipe,
		Params:            ys.Params,
		When:              ys.When,
		Parallel:          ys.Parallel,
		ParallelLimit:     ys.ParallelLimit,
		Target:            ys.Target,
		SubWorkflowInputs: ys.Inputs,
	}
	if ys.Timeout != "" {
		d, err := time.ParseDuration(ys.Timeout)
		if err != nil {
			return Step{}, fmt.Errorf("workflow: %s timeout: %w", where, err)
		}
		step.Timeout = d
	}
	if ys.Retry != nil {
		rp := &RetryPolicy{Max: ys.Retry.Max, Exponential: ys.Retry.Exponential}
		if ys.Retry.Backoff != "" {
			d, err := time.ParseDuration(ys.Retry.Backoff)
			if err != nil {
				return Step{}, fmt.Errorf("workflow: %s retry.backoff: %w", where, err)
			}
			rp.Backoff = d
		}
		if ys.Retry.MaxBackoff != "" {
			d, err := time.ParseDuration(ys.Retry.MaxBackoff)
			if err != nil {
				return Step{}, fmt.Errorf("workflow: %s retry.max_backoff: %w", where, err)
			}
			rp.MaxBackoff = d
		}
		step.Retry = rp
	}
	if ys.Compensate != nil {
		comp := &CompensateAction{
			Command: ys.Compensate.Command,
			Recipe:  ys.Compensate.Recipe,
			Params:  ys.Compensate.Params,
		}
		if ys.Compensate.Timeout != "" {
			d, err := time.ParseDuration(ys.Compensate.Timeout)
			if err != nil {
				return Step{}, fmt.Errorf("workflow: %s compensate.timeout: %w", where, err)
			}
			comp.Timeout = d
		}
		step.Compensate = comp
	}
	if ys.Workflow != "" {
		sw, err := resolveSubWorkflow(ys.Workflow, baseDir, ctx)
		if err != nil {
			return Step{}, fmt.Errorf("workflow: %s: %w", where, err)
		}
		step.Workflow = sw
	}
	if ys.ParallelGroup != nil {
		pg, err := ys.ParallelGroup.toParallelGroup(where, baseDir, ctx)
		if err != nil {
			return Step{}, err
		}
		step.ParallelGroup = pg
	}
	return step, nil
}

// toParallelGroup 把 yaml parallel_group 展开成 workflow.ParallelGroup。
//
// 递归展开每个 branch 里的 step；子 workflow 的路径栈通过 ctx 继承，
// 因此 branch 内的 step.workflow 也会走完整的循环检测与深度限制。
func (yg *yamlParallelGroup) toParallelGroup(where string, baseDir string, ctx *loaderContext) (*ParallelGroup, error) {
	pg := &ParallelGroup{
		ParallelLimit: yg.ParallelLimit,
	}
	if len(yg.Branches) > 0 {
		pg.Branches = make([]Branch, 0, len(yg.Branches))
	}
	for bi, yb := range yg.Branches {
		branch := Branch{
			Name:   yb.Name,
			Target: yb.Target,
		}
		branchWhere := fmt.Sprintf("%s branch[%d]", where, bi)
		if yb.Name != "" {
			branchWhere = fmt.Sprintf("%s branch %q", where, yb.Name)
		}
		if len(yb.Steps) > 0 {
			branch.Steps = make([]Step, 0, len(yb.Steps))
		}
		for si, ys := range yb.Steps {
			st, err := ys.toStep(fmt.Sprintf("%s step[%d]", branchWhere, si), baseDir, ctx)
			if err != nil {
				return nil, err
			}
			branch.Steps = append(branch.Steps, st)
		}
		pg.Branches = append(pg.Branches, branch)
	}
	return pg, nil
}

// compileInputSchema 把用户在 inputs[].schema 里写的**标量或 object** JSON Schema，
// 包装成 core/plugin/settings 需要的"顶层 object"格式再编译。
//
// 复用 settings 编译器的原因：一份代码同时服务插件 settingsSchema 与 workflow inputs，
// 避免重复维护校验 / 默认 / redact 三套逻辑（详见 v0.6.0 RFC §3.5）。
//
// 数据流：
//   1. 把用户 schema 包成 `{ "type": "object", "properties": { "value": <user> }, "required": ["value"] }`
//   2. 交给 settings.Compile 得到 Schema
//   3. Runner 侧校验 input 值时构造 `{ "value": <inputVal> }` 传给 Schema.Validate；
//      ApplyDefaults / Redact 同理走 "value" 路径。P0 只落地编译；求值走 P3 阶段。
//
// 用户 schema 里已经带 required / properties 时，直接透传给 settings.Compile；
// 只有当用户写的是"标量约束"（type: integer/string/boolean/number/array）时才做包装。
func compileInputSchema(userSchema map[string]any) (*settings.Schema, error) {
	// 判断用户是否已经写了 object 顶层。
	if t, ok := userSchema["type"].(string); ok && t == "object" {
		raw, err := json.Marshal(userSchema)
		if err != nil {
			return nil, fmt.Errorf("marshal schema: %w", err)
		}
		return settings.Compile(raw)
	}
	// 否则包装：{ type: object, properties: { value: <user> }, required: [value] }
	wrapped := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"value": userSchema,
		},
		"required": []any{"value"},
	}
	raw, err := json.Marshal(wrapped)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return settings.Compile(raw)
}

// resolveSubWorkflow 处理 step.workflow 引用：递归加载 + 循环检测 + 深度限制。
//
// 支持形态：
//   - "ref://<id>"       → 保留位；Validate 阶段拒绝（v0.6.1+ 走 Registry）
//   - 绝对路径           → 直接拒绝（防止 /etc/foo.yaml 类误用）
//   - 相对路径           → 相对 baseDir 解析；递归 loadReader
//
// 循环检测：当前解析栈里已包含目标 abs path → SUBWORKFLOW_CYCLE。
// 深度限制：depth+1 > MaxSubWorkflowDepth → SUBWORKFLOW_DEPTH_EXCEEDED。
func resolveSubWorkflow(ref string, baseDir string, ctx *loaderContext) (*SubWorkflow, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return nil, errors.New("workflow: sub-workflow reference is empty")
	}
	if strings.HasPrefix(trimmed, "ref://") {
		// 保留位；Validate 会拒。此处不加载 Loaded，让 Validate 报出稳定错误码。
		return &SubWorkflow{Ref: trimmed}, nil
	}
	if filepath.IsAbs(trimmed) {
		return nil, fmt.Errorf("workflow: absolute paths are forbidden for sub-workflow %q (WORKFLOW_ABS_PATH_FORBIDDEN); use relative path", trimmed)
	}
	if ctx == nil {
		ctx = &loaderContext{}
	}
	if ctx.depth+1 > MaxSubWorkflowDepth {
		return nil, fmt.Errorf("workflow: sub-workflow depth %d exceeds cap %d (SUBWORKFLOW_DEPTH_EXCEEDED)", ctx.depth+1, MaxSubWorkflowDepth)
	}
	if baseDir == "" {
		return nil, fmt.Errorf("workflow: sub-workflow %q requires a baseDir; consider using LoadFile or a path-aware loader", trimmed)
	}
	abs, err := filepath.Abs(filepath.Join(baseDir, trimmed))
	if err != nil {
		return nil, fmt.Errorf("workflow: sub-workflow %q: abs: %w", trimmed, err)
	}
	// 循环检测：当前栈里已有此 abs → 拒。
	for _, prev := range ctx.stack {
		if prev == abs {
			return nil, fmt.Errorf("workflow: sub-workflow cycle detected: %s (SUBWORKFLOW_CYCLE)", abs)
		}
	}
	// 递归加载
	f, err := os.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("workflow: open sub-workflow %q: %w", trimmed, err)
	}
	defer f.Close()
	child := &loaderContext{
		stack: append(append([]string(nil), ctx.stack...), abs),
		depth: ctx.depth + 1,
	}
	loaded, err := loadReader(f, filepath.Dir(abs), child)
	if err != nil {
		return nil, fmt.Errorf("workflow: load sub-workflow %q: %w", trimmed, err)
	}
	return &SubWorkflow{Path: trimmed, Loaded: loaded}, nil
}
