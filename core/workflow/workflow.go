// workflow.go 定义 Workflow 引擎的核心数据结构。
//
// v0.2 边界（PR1 骨架）：
//   - 声明式数据模型：Workflow / Step / Input
//   - Validate 只做静态字段校验，不涉及执行、变量求值、依赖拓扑
//   - Step 支持二选一：command（单条 Command）或 recipe（引用已注册 Recipe）
//
// 未来（后续 PR）：
//   - YAML 解析 / Loader
//   - 变量表达式（expr-lang）与 params 求值
//   - 分支 / 并行 / 重试 / 回滚 / 通知
//   - 与 core/recipe.Runner 合流
//
// 详见 docs/workflow.md。

package workflow

import (
	"errors"
	"fmt"
	"time"
)

// InputType 描述 Workflow 声明的输入参数类型。
//
// v0.2 仅收敛为最小可用集合；未列举的字符串一律视作非法。
type InputType string

const (
	InputTypeString InputType = "string"
	InputTypeInt    InputType = "int"
	InputTypeBool   InputType = "bool"
	InputTypeFile   InputType = "file"
)

// Input 描述 Workflow 的一个输入参数。
type Input struct {
	// Name：变量名，${name} 引用；必填且在同一 Workflow 内唯一。
	Name string
	// Type：受支持的 InputType；空值等价于 InputTypeString。
	Type InputType
	// Required：true 表示执行时必须提供；false 时可缺省。
	Required bool
	// Default：默认值（Required=true 时可留空）。
	Default any
	// Description：给 UI/CLI 展示用的说明文字。
	Description string
}

// Step 是 Workflow 的一个执行节点。
//
// Command 与 Recipe 必须二选一：
//   - Command：形如 "ssh.exec"，指向 CommandEngine 中的单条 Command
//   - Recipe ：形如 "system.cpu"，指向 recipe.Registry 中的一个 Recipe
type Step struct {
	// ID：Step 内唯一标识，用于日志/结果映射；不影响执行顺序。
	ID string

	// Command：全限定 command id，格式 "<plugin>.<command>"。
	Command string
	// Recipe：recipe id，对应 recipe.Registry。
	Recipe string

	// Params：Command / Recipe 的输入参数（尚未做变量求值）。
	Params map[string]any

	// Timeout：单步超时；0 表示走底层默认。
	Timeout time.Duration

	// When 是可选的条件表达式（expr-lang 语法，等价于 ${...} 内部）。
	//
	// 语义：
	//   - 为空 → 无条件执行（默认）
	//   - 非空 → 走 expr 求值 + 布尔判定；true 执行，false 跳过（Skipped）
	//   - 求值失败 → Step 记 ErrorCode="WHEN_EVAL" 并中断 Workflow
	//
	// 允许引用与 Params 同款作用域：${inputs.*}、${steps.<id>.out.*}。
	// 与 Params 不同的是这里不需要 ${} 包裹（整串就是表达式）。
	When string

	// Retry 是可选的重试策略；nil 或 Max<=1 表示不重试。
	//
	// 语义边界（见 docs/workflow.md §7.4.2）：
	//   - Max 是"总尝试次数含首次"；1 = 不重试
	//   - 仅对执行阶段错误重试；WHEN_EVAL / INTERPOLATE / NO_EXECUTOR / INVALID_STEP 不重试
	//   - ctx 取消 / 超时会打断 backoff 睡眠，立即返回最后一次执行错误
	Retry *RetryPolicy
}

// RetryPolicy 描述一个 Step 的重试策略。
//
// 保守设计：
//   - v0.3 第二批只做 fixed / exponential（× 2）两种退避
//   - 不支持 jitter（避免复杂化）
//   - 不区分错误类型选择性重试；由 Runner 统一在"仅执行失败"这一层过滤
type RetryPolicy struct {
	// Max 是总尝试次数（含首次）。合法范围 [1, 20]；0 / 缺省 = 1 = 不重试。
	Max int

	// Backoff 是每次失败后的等待时长；0 表示不等待，立即重试。
	Backoff time.Duration

	// MaxBackoff 是 exponential 场景下的时长封顶；0 表示不封顶。
	// 对 fixed 场景无意义。
	MaxBackoff time.Duration

	// Exponential 为 true 时每次退避 × 2（capped by MaxBackoff）；
	// false 时始终使用 Backoff。
	Exponential bool
}

// Validate 校验 RetryPolicy 的静态字段。允许 nil（等价于不重试）。
func (p *RetryPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if p.Max < 0 {
		return errors.New("retry.max must be >= 0")
	}
	if p.Max > 20 {
		return fmt.Errorf("retry.max=%d exceeds hard cap 20", p.Max)
	}
	if p.Backoff < 0 {
		return errors.New("retry.backoff must be >= 0")
	}
	if p.MaxBackoff < 0 {
		return errors.New("retry.max_backoff must be >= 0")
	}
	if p.MaxBackoff > 0 && p.Backoff > p.MaxBackoff {
		return errors.New("retry.backoff must be <= retry.max_backoff")
	}
	if p.Exponential && p.Backoff == 0 {
		return errors.New("retry.exponential=true requires retry.backoff > 0")
	}
	return nil
}

// attempts 返回策略下的最大尝试次数（含首次）。
func (p *RetryPolicy) attempts() int {
	if p == nil || p.Max <= 1 {
		return 1
	}
	return p.Max
}

// nextBackoff 计算第 attempt 次失败后应等待的时长（attempt 从 1 开始）。
//
// attempt=1 表示"首次失败后即将开始的第一次退避"，返回 Backoff。
// exponential 时：第 n 次退避 = Backoff * 2^(n-1)，被 MaxBackoff 封顶。
func (p *RetryPolicy) nextBackoff(attempt int) time.Duration {
	if p == nil || attempt < 1 || p.Backoff <= 0 {
		return 0
	}
	if !p.Exponential {
		return p.Backoff
	}
	// exponential：shift 位运算避免 float
	d := p.Backoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if p.MaxBackoff > 0 && d >= p.MaxBackoff {
			return p.MaxBackoff
		}
	}
	if p.MaxBackoff > 0 && d > p.MaxBackoff {
		return p.MaxBackoff
	}
	return d
}

// Workflow 是一次完整的编排声明。
type Workflow struct {
	ID          string
	Name        string
	Description string

	// Inputs：Workflow 级输入参数；顺序不敏感，Name 唯一。
	Inputs []Input

	// Steps：按声明顺序执行；ID 唯一。
	Steps []Step
}

// Validate 校验 Workflow 的静态字段。
//
// 覆盖：
//   - workflow.id 非空
//   - inputs.name 非空且唯一；type 合法
//   - steps 非空；step.id 非空且唯一
//   - 每个 step 必须且仅能提供 command / recipe 中的一个
//   - step.timeout 不能为负
func (w *Workflow) Validate() error {
	if w == nil {
		return errors.New("workflow is nil")
	}
	if w.ID == "" {
		return errors.New("workflow id is empty")
	}

	if err := validateInputs(w.Inputs); err != nil {
		return err
	}

	if len(w.Steps) == 0 {
		return errors.New("workflow has no steps")
	}
	seen := make(map[string]struct{}, len(w.Steps))
	for i, s := range w.Steps {
		if s.ID == "" {
			return fmt.Errorf("step[%d]: id is empty", i)
		}
		if _, dup := seen[s.ID]; dup {
			return fmt.Errorf("step[%d]: duplicate id %q", i, s.ID)
		}
		seen[s.ID] = struct{}{}

		switch {
		case s.Command == "" && s.Recipe == "":
			return fmt.Errorf("step %q: command or recipe required", s.ID)
		case s.Command != "" && s.Recipe != "":
			return fmt.Errorf("step %q: command and recipe are mutually exclusive", s.ID)
		}

		if s.Timeout < 0 {
			return fmt.Errorf("step %q: timeout must be >= 0", s.ID)
		}
		if err := s.Retry.Validate(); err != nil {
			return fmt.Errorf("step %q: %w", s.ID, err)
		}
	}
	return nil
}

func validateInputs(inputs []Input) error {
	seen := make(map[string]struct{}, len(inputs))
	for i, in := range inputs {
		if in.Name == "" {
			return fmt.Errorf("input[%d]: name is empty", i)
		}
		if _, dup := seen[in.Name]; dup {
			return fmt.Errorf("input[%d]: duplicate name %q", i, in.Name)
		}
		seen[in.Name] = struct{}{}

		if in.Type != "" && !isValidInputType(in.Type) {
			return fmt.Errorf("input %q: unsupported type %q", in.Name, in.Type)
		}
	}
	return nil
}

func isValidInputType(t InputType) bool {
	switch t {
	case InputTypeString, InputTypeInt, InputTypeBool, InputTypeFile:
		return true
	}
	return false
}
