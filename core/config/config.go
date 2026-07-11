// Package config 提供 MOW 的配置加载。
//
// v0.1 采用纯 JSON 解析（标准库），避免过早引入 Viper / TOML 依赖。
// 未来引入 TOML 时保持本包的公开 API 不变，仅替换解码器实现。
//
// 详见 docs/architecture.md。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config 是 MOW 的运行时配置。
type Config struct {
	// Version is the on-disk config schema version. Files created before v0.4.1
	// omitted it and are migrated from legacy version 0 during Load.
	Version int `json:"version"`

	// App 全局应用设置
	App AppConfig `json:"app"`

	// Logger 日志配置（对齐 core/logger.Options 的公开字段）
	Logger LoggerConfig `json:"logger"`

	// AI 是宿主侧 AI orchestrator 的可选配置（v0.4）。
	// 与 plugins.ai.settings 是两码事：这里只描述宿主如何编排（allowlist / 上限），
	// provider 私有配置仍走 plugins.ai.settings.providers[]。
	AI AIConfig `json:"ai"`

	// Plugins 插件的 Enable / 设置
	Plugins map[string]PluginConfig `json:"plugins"`
}

const CurrentVersion = 1

// AIConfig 是宿主侧 AI orchestrator 的可选配置。
//
// 全部字段为可选：留空即使用 core/ai 的安全默认（见 ai.Options 注释）。
// 特别地，AllowedTools 为空 → 模型看不到任何工具，等价于「纯对话模式」，
// 这是 v0.4 的推荐初始状态：用户显式列出白名单后才开放 tool-use。
type AIConfig struct {
	// AllowedTools 是 orchestrator 允许模型调用的 Command 全限定 ID 列表
	// （例："system.cpu"、"docker.list"）。Orchestrator 会在初始化时逐一
	// 校验其为 PermRead / 非流式 / 非 ai.*，否则拒绝构造。
	AllowedTools []string `json:"allowed_tools,omitempty"`

	// MaxRounds / MaxCallsPerRound / MaxTotalCalls / MaxResultBytes / TimeoutSeconds
	// 全部为 0 → 走 orchestrator 默认（8 / 4 / MaxRounds*MaxCallsPerRound / 64KiB / 120s）。
	MaxRounds        int `json:"max_rounds,omitempty"`
	MaxCallsPerRound int `json:"max_calls_per_round,omitempty"`
	MaxTotalCalls    int `json:"max_total_calls,omitempty"`
	MaxResultBytes   int `json:"max_result_bytes,omitempty"`
	TimeoutSeconds   int `json:"timeout_seconds,omitempty"`
}

// AppConfig 是 App 全局设置。
type AppConfig struct {
	// DataDir 是 MOW 的数据根目录（日志 / 审计 / 插件持久化）。
	// 默认取用户目录下的 .mow/。
	DataDir string `json:"data_dir"`

	// PluginsDir 是官方 / 第三方 Plugin 可执行文件所在目录。
	PluginsDir string `json:"plugins_dir"`
}

// LoggerConfig 与 core/logger.Options 对齐。
type LoggerConfig struct {
	Level     string `json:"level"`      // debug / info / warn / error
	Format    string `json:"format"`     // json / text
	AddSource bool   `json:"add_source"` // 是否记录调用位置
}

// PluginConfig 是单个插件的用户设置。
type PluginConfig struct {
	Enabled  bool            `json:"enabled"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

// Default 返回一份合理的默认配置。
func Default() Config {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".mow")
	return Config{
		Version: CurrentVersion,
		App: AppConfig{
			DataDir:    dataDir,
			PluginsDir: filepath.Join(dataDir, "plugins"),
		},
		Logger: LoggerConfig{
			Level:  "info",
			Format: "json",
		},
		Plugins: map[string]PluginConfig{},
	}
}

// Load 从指定路径读取并解析配置。
// 若 path 为空或文件不存在，则返回 Default()（不视为错误）。
func Load(path string) (Config, error) {
	if path == "" {
		return Default(), nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}

	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if cfg.Plugins == nil {
		cfg.Plugins = map[string]PluginConfig{}
	}
	if cfg.Version == 0 {
		// v0.1-v0.4 used the same JSON fields without a schema version. Applying
		// defaults before Unmarshal already supplies newly introduced fields.
		cfg.Version = CurrentVersion
	}
	if cfg.Version > CurrentVersion {
		return Config{}, fmt.Errorf("config: schema version %d is newer than supported version %d", cfg.Version, CurrentVersion)
	}
	return cfg, nil
}

// Save 将配置以 JSON 格式写入 path（若父目录不存在会自动创建）。
func Save(path string, cfg Config) error {
	if path == "" {
		return errors.New("config: path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	if cfg.Version == 0 {
		cfg.Version = CurrentVersion
	}
	if cfg.Version > CurrentVersion {
		return fmt.Errorf("config: cannot save unsupported schema version %d", cfg.Version)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}
