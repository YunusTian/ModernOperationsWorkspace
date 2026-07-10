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
	"strings"
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

	// Compensate 是可选的补偿动作，用于 Workflow 顶层 on_failure.rollback 触发时"撤销自己"。
	//
	// 语义（见 docs/workflow.md §7.4.4）：
	//   - 只在 Workflow 主流程失败、且该 step 已经"执行成功"过时才会被调用
	//   - Skipped / 失败 / 未执行的 step 都不会走 compensate
	//   - Compensate 内部 command / recipe 二选一，与 Step 声明结构相同
	//   - Compensate 内部失败不再触发嵌套 rollback；rollback 中的 step 也不 retry
	Compensate *CompensateAction

	// Parallel 标记该 step 属于一个并行组。
	//
	// 归组规则（见 docs/workflow.md §7.4.5）：
	//   - 声明顺序上**连续**为 true 的 step 归为同一个并行组
	//   - 与前后 Parallel=false 的 step 之间保持顺序执行
	//   - 一组内的 step 并发调度，等整组结束再进入下一组
	//   - fail-fast：组内任一 step 失败会取消同组其它 step 的 ctx
	//
	// 变量作用域约束：
	//   - 组内 step **不允许** 引用同组兄弟的 ${steps.<id>.out.*}
	//   - Validate 会静态拒绝这类引用，避免并发无序导致的隐性 bug
	Parallel bool
}

// CompensateAction 描述一次补偿动作（rollback 时执行）。
//
// 独立结构而非复用 Step，是为了明确"这不是一个可编排的 step"——
// 它没有 ID / When / Retry / Compensate，避免递归纠缠。
type CompensateAction struct {
	// Command / Recipe 二选一，与 Step 声明保持一致。
	Command string
	Recipe  string
	// Params 允许 ${inputs.*} / ${steps.<id>.out.*} 插值，
	// 但只有原 step 已成功时才会执行，因此引用自身 out 是安全的。
	Params  map[string]any
	Timeout time.Duration
}

// Validate 校验 CompensateAction 静态字段。nil 视为未声明。
func (c *CompensateAction) Validate() error {
	if c == nil {
		return nil
	}
	switch {
	case c.Command == "" && c.Recipe == "":
		return errors.New("compensate: command or recipe required")
	case c.Command != "" && c.Recipe != "":
		return errors.New("compensate: command and recipe are mutually exclusive")
	}
	if c.Timeout < 0 {
		return errors.New("compensate: timeout must be >= 0")
	}
	return nil
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

	// OnFailure：Workflow 主流程失败后的补偿声明。nil 表示不做任何补偿。
	OnFailure *FailurePolicy
}

// FailurePolicy 描述 Workflow 失败后要做的事。v0.3 第四批仅支持 rollback。
type FailurePolicy struct {
	// Rollback 是按声明顺序列出的 step id 列表；Runner 会**逆序**遍历，
	// 只对该列表中"已经成功执行过"的 step 调用 Compensate。
	//
	// 允许列表为空 —— 语义上等价于没有 OnFailure。
	// 允许列表包含没有声明 Compensate 的 step id —— 静默跳过。
	Rollback []string
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
		if err := s.Compensate.Validate(); err != nil {
			return fmt.Errorf("step %q: %w", s.ID, err)
		}
	}
	if err := validateOnFailure(w.OnFailure, seen); err != nil {
		return err
	}
	if err := validateParallelGroups(w.Steps); err != nil {
		return err
	}
	return nil
}

// validateOnFailure 校验 rollback 中的 step id 都真实存在。
// 允许列表为空、允许引用没有 Compensate 的 step（Runner 侧静默跳过）。
func validateOnFailure(p *FailurePolicy, stepIDs map[string]struct{}) error {
	if p == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(p.Rollback))
	for i, id := range p.Rollback {
		if id == "" {
			return fmt.Errorf("on_failure.rollback[%d]: empty id", i)
		}
		if _, ok := stepIDs[id]; !ok {
			return fmt.Errorf("on_failure.rollback[%d]: unknown step id %q", i, id)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("on_failure.rollback: duplicate id %q", id)
		}
		seen[id] = struct{}{}
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

// -----------------------------------------------------------------------------
// Parallel 组静态校验
// -----------------------------------------------------------------------------

// ParallelGroup 是一段连续的 step 索引；单元素也是"退化的组"。
type parallelGroup struct {
	// Indices 是该组在 Workflow.Steps 中的索引集合（保持声明顺序）。
	Indices []int
	// Parallel 表示该组是否真正并发（false = 单个顺序 step）。
	Parallel bool
}

// computeGroups 把 Steps 切成"顺序段 + 并行段"。
//
// 规则：连续 Parallel=true 的 step 归为一个并行组；单个 Parallel=false 的 step
// 各自单独成组。返回顺序与 Steps 声明顺序一致。
func computeGroups(steps []Step) []parallelGroup {
	if len(steps) == 0 {
		return nil
	}
	var groups []parallelGroup
	i := 0
	for i < len(steps) {
		if !steps[i].Parallel {
			groups = append(groups, parallelGroup{Indices: []int{i}})
			i++
			continue
		}
		// 收集连续 Parallel=true
		j := i
		for j < len(steps) && steps[j].Parallel {
			j++
		}
		idxs := make([]int, 0, j-i)
		for k := i; k < j; k++ {
			idxs = append(idxs, k)
		}
		// 单个 Parallel=true 也归为并行组（但只有一个成员时相当于顺序执行；
		// 特意不做"降级为顺序"的优化，让语义与用户声明一致，便于测试断言）。
		groups = append(groups, parallelGroup{Indices: idxs, Parallel: true})
		i = j
	}
	return groups
}

// validateParallelGroups 拒绝组内 step 相互引用 out.* 的场景。
//
// 检查范围：Step.Params / Step.When / Step.Compensate.Params 中的字符串。
// 只做简易子串扫描 `steps.<siblingID>.out`，覆盖 `${...}` 与 when 表达式两处入口。
// 这不是完整的 expr 语法分析，但足以拦住"最坑"的并发无序引用。
func validateParallelGroups(steps []Step) error {
	groups := computeGroups(steps)
	for _, g := range groups {
		if !g.Parallel || len(g.Indices) < 2 {
			continue
		}
		// 组内成员的 id 集合
		members := make(map[string]struct{}, len(g.Indices))
		for _, idx := range g.Indices {
			members[steps[idx].ID] = struct{}{}
		}
		for _, idx := range g.Indices {
			s := steps[idx]
			// 扫描 params / when / compensate.params
			bad := findSiblingRef(s.Params, members, s.ID)
			if bad == "" {
				bad = findSiblingRefStr(s.When, members, s.ID)
			}
			if bad == "" && s.Compensate != nil {
				bad = findSiblingRef(s.Compensate.Params, members, s.ID)
			}
			if bad != "" {
				return fmt.Errorf(
					"step %q in parallel group references sibling step %q via steps.%s.out (forbidden — parallel order is undefined)",
					s.ID, bad, bad,
				)
			}
		}
	}
	return nil
}

// findSiblingRef 递归扫描 v（可能是 map / slice / string）中的 "steps.<id>.out" 引用。
// 命中同组其它成员时返回被引用的 id；否则返回 ""。
func findSiblingRef(v any, members map[string]struct{}, selfID string) string {
	switch x := v.(type) {
	case string:
		return findSiblingRefStr(x, members, selfID)
	case map[string]any:
		for _, val := range x {
			if id := findSiblingRef(val, members, selfID); id != "" {
				return id
			}
		}
	case []any:
		for _, val := range x {
			if id := findSiblingRef(val, members, selfID); id != "" {
				return id
			}
		}
	}
	return ""
}

// findSiblingRefStr 在字符串里搜 "steps.<id>." 形式的引用。
//
// 只要出现同组成员（除自己）就命中；此处不做花括号闭合校验——保守拒绝优于漏网。
func findSiblingRefStr(s string, members map[string]struct{}, selfID string) string {
	if s == "" {
		return ""
	}
	const prefix = "steps."
	i := 0
	for {
		idx := indexAt(s, prefix, i)
		if idx < 0 {
			return ""
		}
		start := idx + len(prefix)
		// 读取 id：直到 '.', 空白, '}' 或结束
		end := start
		for end < len(s) {
			c := s[end]
			if c == '.' || c == '}' || c == ' ' || c == '\t' || c == ')' || c == ',' {
				break
			}
			end++
		}
		id := s[start:end]
		if id != "" && id != selfID {
			if _, ok := members[id]; ok {
				return id
			}
		}
		i = end
	}
}

// indexAt 是 strings.Index 的 offset 版本；避免额外分配。
func indexAt(s, sub string, from int) int {
	if from < 0 {
		from = 0
	}
	if from >= len(s) {
		return -1
	}
	if i := strings.Index(s[from:], sub); i >= 0 {
		return from + i
	}
	return -1
}
