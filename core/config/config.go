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
	// App 全局应用设置
	App AppConfig `json:"app"`

	// Logger 日志配置（对齐 core/logger.Options 的公开字段）
	Logger LoggerConfig `json:"logger"`

	// Plugins 插件的 Enable / 设置
	Plugins map[string]PluginConfig `json:"plugins"`
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
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}
