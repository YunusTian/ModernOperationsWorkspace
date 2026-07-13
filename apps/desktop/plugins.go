// plugins.go —— 桌面客户端插件管理绑定（v0.5.1）。
//
// 通过 core/plugin.Lifecycle 复用 CLI 的插件生命周期实现，UI 只做「读列表 +
// 触发操作」两件事：
//   - ListPlugins：合并 Lifecycle.List() 与 Doctor()，附带兼容性检查
//   - EnablePlugin / DisablePlugin：只改 .state，不加载子进程
//   - UninstallPlugin：删除包目录，可选清除 state
//
// 兼容性错误来自 sdk/manifest.CheckCompatibility；诊断错误直接来自
// manifest.ValidatePackage 的错误链。UI 展示两者的差异化 badge。
package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	coreplugin "github.com/mow/mow/core/plugin"
	"github.com/mow/mow/core/plugin/settings"
	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/manifest"
	sdkversion "github.com/mow/mow/sdk/version"
)

// PluginVM 是暴露给前端的插件视图。
type PluginVM struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Version     string `json:"version"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	InstalledAt string `json:"installed_at,omitempty"`
	// PackageDir 是安装位置（绝对路径），便于「在文件管理器中打开」这类操作。
	PackageDir string `json:"package_dir"`

	// Health 汇总健康状态：ok / incompatible / broken。
	Health string `json:"health"`
	// HealthError 是简要错误消息（供列表内展示）。
	HealthError string `json:"health_error,omitempty"`
	// HealthCode 是稳定错误码（PLUGIN_INCOMPATIBLE / PLUGIN_CHECKSUM_MISMATCH 等）。
	HealthCode string `json:"health_code,omitempty"`
	// HealthDetails 是错误详情（layer / actual / constraint / path …）。
	HealthDetails map[string]any `json:"health_details,omitempty"`

	// Commands 是 Manifest 层声明的命令名列表（不含子进程运行时数据）。
	Commands []string `json:"commands,omitempty"`
	// CompatibilityCore 是 Manifest 声明的 core 约束，UI 直接展示。
	CompatibilityCore string `json:"compatibility_core,omitempty"`

	// Platform 是当前二进制适配到的 OS/Arch（无匹配 → 空）。
	Platform string `json:"platform,omitempty"`
}

// ListPlugins 返回全部已安装插件的运行时视图。
//
// 语义：
//   - 优先使用 Manifest 内的 name / author / description / commands / compatibility
//   - Health = ok 表示：包结构+checksum 合法 & 与当前 core/sdk/protocol 版本兼容
//   - Health = incompatible：兼容性约束不满足；HealthCode=PLUGIN_INCOMPATIBLE
//   - Health = broken：ValidatePackage 报错（缺 entrypoint / checksum mismatch 等）
func (a *App) ListPlugins() ([]PluginVM, error) {
	lifecycle, err := coreplugin.NewLifecycle(a.cfg.App.PluginsDir)
	if err != nil {
		return nil, err
	}
	items, err := lifecycle.List()
	if err != nil {
		return nil, err
	}
	diagnostics, dErr := lifecycle.Doctor()
	if dErr != nil {
		// Doctor 本身失败不阻断列表，只是不能补 health。
		a.log.WithComponent("plugin.desktop").Warn("doctor failed", "err", dErr.Error())
	}
	dMap := make(map[string]coreplugin.Diagnostic, len(diagnostics))
	for _, d := range diagnostics {
		dMap[d.ID] = d
	}

	out := make([]PluginVM, 0, len(items))
	for _, it := range items {
		vm := PluginVM{
			ID:         it.ID,
			Version:    it.Version,
			Enabled:    it.Enabled,
			PackageDir: filepath.Join(a.cfg.App.PluginsDir, it.ID),
			Health:     "ok",
		}
		if !it.Installed.IsZero() {
			vm.InstalledAt = it.Installed.UTC().Format("2006-01-02T15:04:05Z")
		}
		// Manifest 元信息
		if mf, err := manifest.Load(vm.PackageDir); err == nil {
			vm.Name = mf.Name
			vm.Author = mf.Author
			vm.Description = mf.Description
			vm.CompatibilityCore = mf.Compatibility.Core
			for _, c := range mf.Commands {
				vm.Commands = append(vm.Commands, c.ID)
			}
			// 匹配当前运行平台。
			for _, p := range mf.Platforms {
				if p.OS == runtime.GOOS && p.Arch == runtime.GOARCH {
					vm.Platform = fmt.Sprintf("%s/%s", p.OS, p.Arch)
					break
				}
			}
			// 兼容性优先判定：若约束不满足，直接报 incompatible。
			protoVer := fmt.Sprintf("%d.0.0", sdk.Handshake.ProtocolVersion)
			if err := mf.CheckCompatibility(sdkversion.Version, sdkversion.Version, protoVer); err != nil {
				vm.Health = "incompatible"
				fillHealthError(&vm, err)
			}
		}
		// Doctor 覆盖：ValidatePackage 报错代表包已损坏。
		if d, ok := dMap[it.ID]; ok && !d.OK {
			vm.Health = "broken"
			vm.HealthError = d.Error
			if vm.HealthCode == "" {
				vm.HealthCode = "PLUGIN_PACKAGE_INVALID"
			}
		}
		out = append(out, vm)
	}
	return out, nil
}

// SetPluginEnabled 切换插件启用状态。禁用时不会立刻卸载已加载的子进程，
// 但 CLI/Desktop 下次冷启动会拒绝启用（与 CLI 语义一致）。
func (a *App) SetPluginEnabled(id string, enabled bool) (PluginVM, error) {
	lifecycle, err := coreplugin.NewLifecycle(a.cfg.App.PluginsDir)
	if err != nil {
		return PluginVM{}, err
	}
	if _, err := lifecycle.SetEnabled(id, enabled); err != nil {
		return PluginVM{}, err
	}
	// 简化：直接重扫一次，让前端拿到最新健康状态。
	return a.pluginByID(id)
}

// UninstallPlugin 删除插件目录；purge=true 时连 .state 也一并删除。
// v0.5.2 P1：purge=true 时同时清理 secret sidecar。
func (a *App) UninstallPlugin(id string, purge bool) error {
	lifecycle, err := coreplugin.NewLifecycle(a.cfg.App.PluginsDir)
	if err != nil {
		return err
	}
	if err := lifecycle.Uninstall(id, purge); err != nil {
		return err
	}
	if purge && a.cfg.App.DataDir != "" {
		if err := settings.NewStoreFromDataDir(a.cfg.App.DataDir).Delete(id); err != nil {
			a.log.WithComponent("plugin.settings").
				Warn("purge secret sidecar failed", "id", id, "err", err.Error())
		}
	}
	return nil
}

// DoctorPlugin 对单个插件做一次深度诊断，返回结构化结果。
func (a *App) DoctorPlugin(id string) (PluginVM, error) {
	return a.pluginByID(id)
}

func (a *App) pluginByID(id string) (PluginVM, error) {
	all, err := a.ListPlugins()
	if err != nil {
		return PluginVM{}, err
	}
	for _, vm := range all {
		if vm.ID == id {
			return vm, nil
		}
	}
	return PluginVM{}, fmt.Errorf("plugin %q not found", id)
}

// fillHealthError 尽可能把 *sdk.Error 的稳定错误码/详情填进 vm。
func fillHealthError(vm *PluginVM, err error) {
	vm.HealthError = err.Error()
	var se *sdk.Error
	if errors.As(err, &se) {
		vm.HealthCode = se.Code
		if len(se.Details) > 0 {
			vm.HealthDetails = map[string]any{}
			for k, v := range se.Details {
				vm.HealthDetails[k] = v
			}
		}
		// 让 UI 简短消息更好读：把 layer / actual / constraint 拼进 error 尾部。
		if se.Details != nil {
			layer, _ := se.Details["layer"].(string)
			actual, _ := se.Details["actual"].(string)
			constraint, _ := se.Details["constraint"].(string)
			if layer != "" && actual != "" && constraint != "" {
				vm.HealthError = fmt.Sprintf("%s %s does not satisfy %q", strings.ToLower(layer), actual, constraint)
			}
		}
	}
}
