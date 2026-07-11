// Package manifest 实现 MOW Plugin Manifest（plugin.json）的解析与校验。
//
// v0.5.0 引入 Manifest 作为插件包（plugin package）的元信息载体，
// 承载：
//   - 插件身份（id / name / version / author / license / homepage）
//   - 兼容范围（Core / SDK / Protocol 三层 semver 约束）
//   - 平台矩阵（os × arch → entrypoint + checksum）
//   - 能力摘要（commands / permissions / connectionTypes）
//   - 用户配置 JSON Schema（settingsSchema，v0.5.2 才驱动 UI）
//   - 资源清单（recipes / workflows 的包内相对路径）
//   - 数据格式版本与迁移入口（dataVersion / migrations）
//   - 发布来源与签名（source / signature）
//
// 本包只做「静态校验 + 兼容范围匹配」，不做下载、安装、执行；
// 安装/生命周期属于 v0.5.1 范围。
//
// 详见 docs/plugin-system.md 与 docs/v0.5.0-acceptance-checklist.md。
package manifest

import "github.com/mow/mow/sdk"

// -----------------------------------------------------------------------------
// 稳定错误码
//
// 所有对外可见的错误都通过 sdk.Error 携带以下 Code；调用方应据 Code 而非
// Message 做条件判断。CHANGELOG 会记录任何 Code 的变更。
// -----------------------------------------------------------------------------

const (
	// ErrCodeManifestInvalid 表示 Manifest JSON 反序列化或结构校验失败。
	// Details 通常包含：
	//   - "field": 定位到字段的 JSON pointer / dotted path
	//   - "reason": 具体原因
	ErrCodeManifestInvalid = "PLUGIN_MANIFEST_INVALID"

	// ErrCodeManifestMismatch 表示 Manifest 与运行时 sdk.Metadata 不一致
	// （例如 id 或 version 对不上）。用于运行时强校验；v0.5.0 由 Core 侧调用。
	ErrCodeManifestMismatch = "PLUGIN_MANIFEST_MISMATCH"

	// ErrCodeIncompatible 表示 compatibility.core / .sdk / .protocol 中至少
	// 一条 semver 约束不满足当前运行环境。Details 中给出具体是哪一层不满足
	// 和实际版本 / 期望范围。
	ErrCodeIncompatible = "PLUGIN_INCOMPATIBLE"

	// ErrCodeChecksumMismatch 表示某个 platforms[].entrypoint 的实际 SHA-256
	// 与 Manifest 声明不符。由 `mow plugin validate` 使用；本包只定义常量。
	ErrCodeChecksumMismatch = "PLUGIN_CHECKSUM_MISMATCH"

	// ErrCodeEntrypointMissing 表示 platforms[].entrypoint 或 recipes/workflows
	// 引用的相对路径在包内不存在。
	ErrCodeEntrypointMissing = "PLUGIN_ENTRYPOINT_MISSING"
)

// newError 是本包内部的错误构造器，统一把 field / reason 塞进 Details。
// 供本包各文件复用，避免 Details map 拼装样板。
func newError(code, message, field, reason string) *sdk.Error {
	e := sdk.NewError(code, message, nil)
	if field != "" || reason != "" {
		details := map[string]any{}
		if field != "" {
			details["field"] = field
		}
		if reason != "" {
			details["reason"] = reason
		}
		e = e.WithDetails(details)
	}
	return e
}
