package manifest

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// -----------------------------------------------------------------------------
// 极简 semver 约束求解器
//
// 为避免为 sdk/ 引入新依赖，我们不使用 github.com/Masterminds/semver。
// v0.5.0 只支持有限但完备的约束语法：
//
//   - 单个版本:            "1.2.3"           等价于 =1.2.3
//   - 明确算子:            ">=0.5.0", "<0.6.0", "=1.0.0", "!=1.2.3"
//   - 逗号 AND 组合:       ">=0.5.0,<0.6.0"
//   - 通配符:              "*"、""            表示任意版本
//
// 不支持 caret (^) / tilde (~) / 前导 v，避免歧义。
//
// pre-release 与 build metadata 的比较遵循 SemVer 2.0.0：
//   - 带 pre-release 的版本 < 同基版本无 pre-release，例如 1.0.0-rc.1 < 1.0.0
//   - build metadata（+xxx）在比较时忽略
// -----------------------------------------------------------------------------

// Constraint 是编译后的约束集，可对 Version 求解 Match/Mismatch。
type Constraint struct {
	raw   string
	terms []constraintTerm
	// wildcard=true 表示 "*" / 空串，匹配任意版本。
	wildcard bool
}

type constraintTerm struct {
	op      string
	version parsedVersion
}

type parsedVersion struct {
	major, minor, patch int
	pre                 []string
	// build metadata 在比较时忽略，仅保留供打印。
	build string
}

// ParseVersion 解析 semver 字符串（不接受前导 v）。
func ParseVersion(s string) (parsedVersion, error) {
	if !semverPattern.MatchString(s) {
		return parsedVersion{}, fmt.Errorf("invalid semver %q", s)
	}
	// 拆分 build metadata
	buildIdx := strings.Index(s, "+")
	var build string
	if buildIdx >= 0 {
		build = s[buildIdx+1:]
		s = s[:buildIdx]
	}
	// 拆分 pre-release
	preIdx := strings.Index(s, "-")
	var pre []string
	if preIdx >= 0 {
		pre = strings.Split(s[preIdx+1:], ".")
		s = s[:preIdx]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return parsedVersion{}, fmt.Errorf("invalid semver %q: expected MAJOR.MINOR.PATCH", s)
	}
	nums := [3]int{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return parsedVersion{}, fmt.Errorf("invalid semver %q: non-numeric part", s)
		}
		nums[i] = n
	}
	return parsedVersion{major: nums[0], minor: nums[1], patch: nums[2], pre: pre, build: build}, nil
}

// String 打印规范形式，用于错误消息。
func (v parsedVersion) String() string {
	base := fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	if len(v.pre) > 0 {
		base += "-" + strings.Join(v.pre, ".")
	}
	if v.build != "" {
		base += "+" + v.build
	}
	return base
}

// compare 遵循 SemVer 2.0.0；返回 -1 / 0 / +1。
func (v parsedVersion) compare(o parsedVersion) int {
	if v.major != o.major {
		return sign(v.major - o.major)
	}
	if v.minor != o.minor {
		return sign(v.minor - o.minor)
	}
	if v.patch != o.patch {
		return sign(v.patch - o.patch)
	}
	// pre-release 规则：有 pre-release 的低于无 pre-release
	if len(v.pre) == 0 && len(o.pre) == 0 {
		return 0
	}
	if len(v.pre) == 0 {
		return 1
	}
	if len(o.pre) == 0 {
		return -1
	}
	// 逐段比较 pre-release
	n := len(v.pre)
	if len(o.pre) < n {
		n = len(o.pre)
	}
	for i := 0; i < n; i++ {
		if c := comparePreRelease(v.pre[i], o.pre[i]); c != 0 {
			return c
		}
	}
	return sign(len(v.pre) - len(o.pre))
}

func comparePreRelease(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	// 都是数字：按数字比较
	if aErr == nil && bErr == nil {
		return sign(an - bn)
	}
	// 一方是数字：数字优先级更低
	if aErr == nil {
		return -1
	}
	if bErr == nil {
		return 1
	}
	// 都是字符串：按 ASCII 比较
	if a == b {
		return 0
	}
	if a < b {
		return -1
	}
	return 1
}

func sign(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	default:
		return 0
	}
}

// -----------------------------------------------------------------------------
// Constraint parsing & matching
// -----------------------------------------------------------------------------

// termPattern 精确匹配 "<op><version>"，op ∈ {>=,<=,>,<,=,!=} 或缺省。
var termPattern = regexp.MustCompile(`^(>=|<=|!=|=|>|<)?\s*(.+)$`)

// ParseConstraint 解析 semver 约束表达式。
// 语法参见文件顶部注释。
func ParseConstraint(expr string) (*Constraint, error) {
	raw := expr
	expr = strings.TrimSpace(expr)
	if expr == "" || expr == "*" {
		return &Constraint{raw: raw, wildcard: true}, nil
	}

	parts := strings.Split(expr, ",")
	terms := make([]constraintTerm, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty term in constraint %q", raw)
		}
		m := termPattern.FindStringSubmatch(part)
		if m == nil {
			return nil, fmt.Errorf("cannot parse term %q", part)
		}
		op := m[1]
		if op == "" {
			op = "="
		}
		versionStr := strings.TrimSpace(m[2])
		v, err := ParseVersion(versionStr)
		if err != nil {
			return nil, fmt.Errorf("term %q: %w", part, err)
		}
		terms = append(terms, constraintTerm{op: op, version: v})
	}
	return &Constraint{raw: raw, terms: terms}, nil
}

// String 返回原始输入，方便日志打印。
func (c *Constraint) String() string {
	if c == nil {
		return ""
	}
	return c.raw
}

// Check 报告 version 是否满足约束。version 允许带 pre-release / build metadata。
func (c *Constraint) Check(version string) (bool, error) {
	if c == nil {
		return false, errors.New("nil constraint")
	}
	if c.wildcard {
		return true, nil
	}
	v, err := ParseVersion(version)
	if err != nil {
		return false, err
	}
	for _, t := range c.terms {
		if !t.match(v) {
			return false, nil
		}
	}
	return true, nil
}

func (t constraintTerm) match(v parsedVersion) bool {
	cmp := v.compare(t.version)
	switch t.op {
	case "=":
		return cmp == 0
	case "!=":
		return cmp != 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	default:
		return false
	}
}

// -----------------------------------------------------------------------------
// 便捷入口
// -----------------------------------------------------------------------------

// CheckCompatibility 一次性校验三层兼容性约束是否都满足。
// coreVersion / sdkVersion / protocolVersion 是运行时侧的实际版本；
// 空字符串表示该层跳过（例如 Manifest 未声明 sdk 约束）。
//
// 校验失败返回带 ErrCodeIncompatible 的 sdk.Error，Details 中给出：
//
//	"layer":       "core" | "sdk" | "protocol"
//	"actual":      运行时版本
//	"constraint":  Manifest 声明的约束
func (m *Manifest) CheckCompatibility(coreVersion, sdkVersion, protocolVersion string) error {
	if err := checkLayer("core", m.Compatibility.Core, coreVersion); err != nil {
		return err
	}
	if err := checkLayer("sdk", m.Compatibility.SDK, sdkVersion); err != nil {
		return err
	}
	if err := checkLayer("protocol", m.Compatibility.Protocol, protocolVersion); err != nil {
		return err
	}
	return nil
}

func checkLayer(layer, constraint, actual string) error {
	if constraint == "" {
		return nil
	}
	if actual == "" {
		return incompatible(layer, constraint, "", "runtime version unavailable")
	}
	c, err := ParseConstraint(constraint)
	if err != nil {
		return newError(
			ErrCodeManifestInvalid,
			fmt.Sprintf("compatibility.%s: %v", layer, err),
			"compatibility."+layer, err.Error(),
		)
	}
	ok, err := c.Check(actual)
	if err != nil {
		return incompatible(layer, constraint, actual, err.Error())
	}
	if !ok {
		return incompatible(layer, constraint, actual, "version does not satisfy constraint")
	}
	return nil
}

func incompatible(layer, constraint, actual, reason string) error {
	e := newError(
		ErrCodeIncompatible,
		fmt.Sprintf("plugin is incompatible: %s %s does not satisfy %q", layer, actual, constraint),
		"compatibility."+layer, reason,
	)
	e = e.WithDetails(map[string]any{
		"layer":      layer,
		"actual":     actual,
		"constraint": constraint,
	})
	return e
}
