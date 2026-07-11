// Package plugin 的 Manifest 门控：v0.5.0 P4
//
// 本文件在 core/plugin 里为插件加载链路增加两道 Manifest 关卡：
//
//  1. 启动子进程之前：CheckCompatibility（Core / SDK / Protocol 三层 semver）
//  2. 启动子进程之后立即：MatchMetadata（Manifest.id/version ⇄ 运行时 Metadata）
//
// 只要任一步失败：
//   - 第 1 步：直接返回错误，不启动子进程（避免为已知不兼容的插件付出进程开销）
//   - 第 2 步：立即 Close 子进程，返回错误（保证进程不泄漏）
//
// 返回错误一律使用 sdk/manifest 的稳定错误码（PLUGIN_INCOMPATIBLE / PLUGIN_MANIFEST_MISMATCH），
// 便于 CLI / Desktop / 审计做条件判断。
package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	hclog "github.com/hashicorp/go-hclog"

	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
	"github.com/mow/mow/sdk/version"
)

// ManifestGate 汇总一次「按 Manifest 加载插件」所需的运行时版本信息。
//
// Zero value 是合法的：CoreVersion 默认取 sdk/version.Version；SDKVersion 同上；
// ProtocolVersion 默认取 sdk.Handshake.ProtocolVersion 的字符串形式。
// 这样常规调用点只需传 nil 或 &ManifestGate{}，无需重复配置。
type ManifestGate struct {
	// CoreVersion 是当前 MOW Core 的版本；用于匹配 Manifest.compatibility.core。
	CoreVersion string
	// SDKVersion 是当前 Plugin SDK 的版本；用于匹配 Manifest.compatibility.sdk。
	SDKVersion string
	// ProtocolVersion 是 Plugin gRPC Protocol 版本（半开区间）。
	// 用于匹配 Manifest.compatibility.protocol。
	ProtocolVersion string
	// Logger 供 pluginclient.LoadFromBinary 使用；nil 时走 hclog.NewNullLogger。
	Logger hclog.Logger
}

// resolve 填入默认值，返回一份可用于 CheckCompatibility 的副本。
func (g *ManifestGate) resolve() ManifestGate {
	out := ManifestGate{}
	if g != nil {
		out = *g
	}
	if out.CoreVersion == "" {
		out.CoreVersion = version.Version
	}
	if out.SDKVersion == "" {
		out.SDKVersion = version.Version
	}
	if out.ProtocolVersion == "" {
		out.ProtocolVersion = fmt.Sprintf("%d.0.0", sdk.Handshake.ProtocolVersion)
	}
	return out
}

// LoadFromPackage 用于「插件包目录 + plugin.json」的加载路径。
//
// 步骤：
//  1. 读取 packageDir/plugin.json 并静态校验 —— 失败返回 PLUGIN_MANIFEST_INVALID
//  2. 用 gate 的三层版本校验 Manifest.compatibility —— 失败返回 PLUGIN_INCOMPATIBLE
//  3. 从 Manifest 选出与当前 OS/ARCH 匹配的 entrypoint —— 缺失返回 PLUGIN_ENTRYPOINT_MISSING
//  4. LoadFromBinary 启动子进程
//  5. MatchMetadata：立即比对 Manifest.id/version 与运行时 Metadata —— 失败关掉子进程
//     并返回 PLUGIN_MANIFEST_MISMATCH
//
// 返回的 LoadedPlugin 可直接传给 Manager.Register；同时返回 Manifest，方便调用方
// 后续持久化或审计。
func LoadFromPackage(packageDir string, gate *ManifestGate) (*LoadedPlugin, *manifest.Manifest, error) {
	// Runtime loading uses the same filesystem and checksum validation as
	// `mow plugin validate`; a package cannot pass CLI validation yet bypass it
	// during actual startup.
	if _, err := manifest.ValidatePackage(packageDir); err != nil {
		return nil, nil, err
	}
	m, err := manifest.Load(packageDir)
	if err != nil {
		return nil, nil, err
	}
	resolved := gate.resolve()
	if err := m.CheckCompatibility(resolved.CoreVersion, resolved.SDKVersion, resolved.ProtocolVersion); err != nil {
		return nil, nil, err
	}

	entry, findErr := resolveEntrypoint(packageDir, m)
	if findErr != nil {
		return nil, nil, findErr
	}

	lp, loadErr := loadBinary(entry, resolved.Logger)
	if loadErr != nil {
		return nil, nil, loadErr
	}

	// 启动完成后立即比对元信息；失败必须 Close，避免子进程泄漏。
	if err := m.MatchMetadata(lp.Plugin.Metadata()); err != nil {
		lp.Close()
		return nil, nil, err
	}

	return lp, m, nil
}

// LoadInstalled resolves the v0.5 package layout first, then falls back to the
// v0.4 flat executable layout for the v0.5.x compatibility window.
//
// Package: <pluginsDir>/<id>/plugin.json + bin/<entrypoint>
// Legacy:  <pluginsDir>/<id>[.exe]
func LoadInstalled(pluginsDir, id string, gate *ManifestGate) (*LoadedPlugin, *manifest.Manifest, bool, error) {
	packageDir := filepath.Join(pluginsDir, id)
	if _, err := os.Stat(filepath.Join(packageDir, manifest.ManifestFileName)); err == nil {
		lp, mf, loadErr := LoadFromPackage(packageDir, gate)
		return lp, mf, false, loadErr
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return nil, nil, false, fmt.Errorf("stat plugin package %q: %w", id, err)
	}

	legacyPath := filepath.Join(pluginsDir, id)
	if runtime.GOOS == "windows" {
		legacyPath += ".exe"
	}
	if _, err := os.Stat(legacyPath); err != nil {
		return nil, nil, false, fmt.Errorf("plugin %q not installed as package (%s) or legacy binary (%s): %w", id, packageDir, legacyPath, err)
	}
	resolved := gate.resolve()
	lp, err := loadBinary(legacyPath, resolved.Logger)
	return lp, nil, true, err
}

// RegisterFromPackage 是 LoadFromPackage + Manager.Register 的组合形式。
// 常规调用点（apps/cli / apps/desktop）应优先使用它，只需传 packageDir。
func (m *Manager) RegisterFromPackage(packageDir string, gate *ManifestGate) (*LoadedPlugin, *manifest.Manifest, error) {
	lp, mf, err := LoadFromPackage(packageDir, gate)
	if err != nil {
		return nil, nil, err
	}
	if regErr := m.Register(lp.Plugin); regErr != nil {
		lp.Close()
		return nil, nil, regErr
	}
	return lp, mf, nil
}

// resolveEntrypoint 用当前进程的 GOOS / GOARCH 从 Manifest 里选一条 platforms[]，
// 并拼出磁盘上的绝对路径。缺失则返回 PLUGIN_ENTRYPOINT_MISSING。
//
// 这里不做 checksum 校验（那是 `mow plugin validate` 的职责）；本函数只关心
// 「能不能拉起来子进程」。
func resolveEntrypoint(packageDir string, m *manifest.Manifest) (string, error) {
	// 允许 packageDir 是 plugin.json 文件路径
	absDir, absErr := filepath.Abs(packageDir)
	if absErr != nil {
		absDir = packageDir
	}
	if info, statErr := os.Stat(absDir); statErr == nil && !info.IsDir() {
		absDir = filepath.Dir(absDir)
	}

	p := m.PlatformFor(currentGOOS(), currentGOARCH())
	if p == nil {
		return "", sdk.NewError(
			manifest.ErrCodeEntrypointMissing,
			fmt.Sprintf("no platform entry for %s/%s", currentGOOS(), currentGOARCH()),
			nil,
		).WithDetails(map[string]any{
			"os":   currentGOOS(),
			"arch": currentGOARCH(),
		})
	}
	full := filepath.Join(absDir, filepath.FromSlash(p.Entrypoint))
	if _, err := os.Stat(full); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", sdk.NewError(
				manifest.ErrCodeEntrypointMissing,
				fmt.Sprintf("entrypoint %s not found", p.Entrypoint),
				err,
			).WithDetails(map[string]any{"path": p.Entrypoint})
		}
		return "", sdk.NewError(
			manifest.ErrCodeEntrypointMissing,
			fmt.Sprintf("stat entrypoint %s: %v", p.Entrypoint, err),
			err,
		)
	}
	return full, nil
}

// currentGOOS / currentGOARCH 允许测试通过 monkey 变量替换出目标平台。
var (
	currentGOOS   = func() string { return runtime.GOOS }
	currentGOARCH = func() string { return runtime.GOARCH }
)

// loadBinary 是 LoadFromBinary 的内部间接层，允许测试替换以避开真实子进程。
// 生产代码不要直接调用 —— 走 LoadFromPackage / RegisterFromPackage。
var loadBinary = func(path string, logger hclog.Logger) (*LoadedPlugin, error) {
	return LoadFromBinary(path, logger)
}
