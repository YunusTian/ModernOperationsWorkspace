// vars.go 实现 Workflow 的变量作用域与字符串插值。
//
// v0.2 PR3：
//   - Scope 提供 inputs / steps 两个命名空间
//   - Interpolate 递归处理 string / map[string]any / []any / [k]any
//   - 占位符语法：${<expr>}，<expr> 走 github.com/expr-lang/expr 求值
//   - 特殊行为：
//     · 整串仅为一个 ${expr} → 返回原始类型（int/bool/slice/... 不会被字符串化）
//     · 混合形态（前后有字面量）→ 逐段替换后拼成字符串
//     · 字符串中不含 ${} → 原样返回
//   - 错误：使用 InterpolationError 携带 offset + 原始表达式，便于 UI 定位
//
// 本文件不引入 Workflow.Step / Runner，仅提供纯粹的插值工具，
// 让 Runner 在后续 PR 中按需组合调用。

package workflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// -----------------------------------------------------------------------------
// Scope
// -----------------------------------------------------------------------------

// StepScope 是单个 Step 在作用域内的可见字段。
//
// Out：Step 完成后暴露给后续 Step 的字段（例如 bytes_sent / exit_code），
// 具体命名由各 Command 的 OutputSchema 决定，Runner 负责填充。
type StepScope struct {
	Out map[string]any
}

// Scope 是 Workflow 执行期的变量作用域。
//
// 字段暴露给表达式使用：
//   - inputs.<name>：Workflow.Inputs 中声明的参数
//   - steps.<step_id>.out.<field>：已完成 Step 的输出
type Scope struct {
	Inputs map[string]any
	Steps  map[string]StepScope
}

// env 将 Scope 转成表达式引擎可见的 map。
func (s Scope) env() map[string]any {
	inputs := s.Inputs
	if inputs == nil {
		inputs = map[string]any{}
	}
	steps := make(map[string]any, len(s.Steps))
	for id, st := range s.Steps {
		out := st.Out
		if out == nil {
			out = map[string]any{}
		}
		steps[id] = map[string]any{"out": out}
	}
	return map[string]any{
		"inputs": inputs,
		"steps":  steps,
	}
}

// -----------------------------------------------------------------------------
// 错误
// -----------------------------------------------------------------------------

// InterpolationError 描述一次插值失败：位置 + 原表达式 + 原因。
type InterpolationError struct {
	Offset int    // 占位符 "${" 在原始字符串中的字节偏移
	Expr   string // ${} 内的表达式内容
	Cause  error  // 底层错误（expr 编译/求值 / 语法）
}

func (e *InterpolationError) Error() string {
	return fmt.Sprintf("interpolate: at offset %d, expr %q: %v", e.Offset, e.Expr, e.Cause)
}

func (e *InterpolationError) Unwrap() error { return e.Cause }

// -----------------------------------------------------------------------------
// Interpolate：入口
// -----------------------------------------------------------------------------

// Interpolate 递归遍历 v，对其中所有 string 做 ${} 插值，返回新值。
//
// 支持的容器：map[string]any、[]any、[N]any（数组按 slice 处理），
// 其他类型（int/bool/float/nil/...）原样返回。原始输入不会被就地修改。
func Interpolate(v any, scope Scope) (any, error) {
	return interpolateValue(v, scope)
}

func interpolateValue(v any, scope Scope) (any, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case string:
		return interpolateString(x, scope)
	case map[string]any:
		return interpolateMap(x, scope)
	case []any:
		return interpolateSlice(x, scope)
	default:
		return v, nil
	}
}

func interpolateMap(m map[string]any, scope Scope) (map[string]any, error) {
	out := make(map[string]any, len(m))
	for k, v := range m {
		nv, err := interpolateValue(v, scope)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		out[k] = nv
	}
	return out, nil
}

func interpolateSlice(s []any, scope Scope) ([]any, error) {
	out := make([]any, len(s))
	for i, v := range s {
		nv, err := interpolateValue(v, scope)
		if err != nil {
			return nil, fmt.Errorf("index %d: %w", i, err)
		}
		out[i] = nv
	}
	return out, nil
}

// -----------------------------------------------------------------------------
// 字符串插值
// -----------------------------------------------------------------------------

// interpolateString 处理单个字符串：
//   - 完全没有 ${} → 原样返回
//   - 整串仅为一个 ${expr} → 返回表达式原始值（保留类型）
//   - 混合形态 → 逐段替换后拼成字符串
func interpolateString(s string, scope Scope) (any, error) {
	if !strings.Contains(s, "${") {
		return s, nil
	}

	// 尝试匹配"整串是一个 ${expr}"
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		end := findClosingBrace(s, 1)
		if end == len(s)-1 {
			exprStr := s[2:end]
			val, err := evalExpr(exprStr, scope, 0)
			if err != nil {
				return nil, err
			}
			return val, nil
		}
	}

	// 混合形态：串接
	var b strings.Builder
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], "${")
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		absStart := i + idx
		b.WriteString(s[i:absStart])
		end := findClosingBrace(s, absStart+1)
		if end < 0 {
			return nil, &InterpolationError{
				Offset: absStart,
				Expr:   s[absStart:],
				Cause:  errors.New("unterminated ${...}"),
			}
		}
		exprStr := s[absStart+2 : end]
		val, err := evalExpr(exprStr, scope, absStart)
		if err != nil {
			return nil, err
		}
		b.WriteString(stringify(val))
		i = end + 1
	}
	return b.String(), nil
}

// findClosingBrace 从 s[startAtBrace] 位置开始寻找与其配对的 '}'。
// 允许嵌套 {} 以支持像 ${a["x"]} 这类表达式。
func findClosingBrace(s string, startAtBrace int) int {
	depth := 0
	inStr := byte(0)
	for i := startAtBrace; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == '\\' && i+1 < len(s) {
				i++
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			inStr = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// stringify 将表达式求值结果转成字符串，用于混合插值场景。
func stringify(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// -----------------------------------------------------------------------------
// 表达式求值
// -----------------------------------------------------------------------------

func evalExpr(exprStr string, scope Scope, offset int) (any, error) {
	trimmed := strings.TrimSpace(exprStr)
	if trimmed == "" {
		return nil, &InterpolationError{
			Offset: offset, Expr: exprStr,
			Cause: errors.New("empty expression"),
		}
	}
	env := scope.env()
	program, err := expr.Compile(trimmed, expr.Env(env))
	if err != nil {
		return nil, &InterpolationError{Offset: offset, Expr: exprStr, Cause: err}
	}
	out, err := runProgram(program, env)
	if err != nil {
		return nil, &InterpolationError{Offset: offset, Expr: exprStr, Cause: err}
	}
	// map[string]any 缺失 key 会静默返回 nil；对 Workflow 场景视为未知变量。
	if out == nil {
		return nil, &InterpolationError{
			Offset: offset, Expr: exprStr,
			Cause: errors.New("undefined variable or nil result"),
		}
	}
	return out, nil
}

func runProgram(p *vm.Program, env any) (any, error) {
	return expr.Run(p, env)
}

// -----------------------------------------------------------------------------
// 独立表达式求值（供 `when` 等无 ${} 包裹场景使用）
// -----------------------------------------------------------------------------

// EvalBool 把整串视为一个 expr 表达式并求成布尔值。
//
// 用于 Step.When：不需要 ${} 包裹，直接写 "inputs.debug == true"。
// 与 Interpolate 内部行为对齐——把结果按 expr-lang 语义强转为 bool。
//
// 出错时返回 InterpolationError（Offset=0，Expr=原串），便于 UI 归一化提示。
func EvalBool(exprStr string, scope Scope) (bool, error) {
	trimmed := strings.TrimSpace(exprStr)
	if trimmed == "" {
		return false, &InterpolationError{
			Offset: 0, Expr: exprStr,
			Cause: errors.New("empty expression"),
		}
	}
	env := scope.env()
	program, err := expr.Compile(trimmed, expr.Env(env), expr.AsBool())
	if err != nil {
		return false, &InterpolationError{Offset: 0, Expr: exprStr, Cause: err}
	}
	out, err := runProgram(program, env)
	if err != nil {
		return false, &InterpolationError{Offset: 0, Expr: exprStr, Cause: err}
	}
	b, ok := out.(bool)
	if !ok {
		return false, &InterpolationError{
			Offset: 0, Expr: exprStr,
			Cause: fmt.Errorf("expected bool, got %T", out),
		}
	}
	return b, nil
}
