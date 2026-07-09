package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/connection"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
)

// -----------------------------------------------------------------------------
// App —— CLI 共享的运行时依赖
// -----------------------------------------------------------------------------

// App 汇总 Logger / Config / ConnectionManager / PluginManager / Engine。
// 一次 CLI 进程只会 Load 一次；未使用的能力不会被初始化。
type App struct {
	Cfg     config.Config
	Log     *logger.Logger
	ConnMgr *connection.Manager
	PlugMgr *plugin.Manager
	Engine  *command.Engine

	// 已加载的插件句柄，退出时统一 Close。
	loaded []func()
}

// appHolder 延迟加载 App —— 避免 --help 等场景做任何 IO。
type appHolder struct {
	configPath *string
	once       sync.Once
	app        *App
	err        error
}

// Load 加载 App；重复调用返回同一实例。
func (h *appHolder) Load() (*App, error) {
	h.once.Do(func() { h.app, h.err = loadApp(*h.configPath) })
	return h.app, h.err
}

func loadApp(cfgPath string) (*App, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if cfg.App.DataDir == "" {
		return nil, fmt.Errorf("config.App.DataDir is empty")
	}
	if err := os.MkdirAll(cfg.App.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir data_dir: %w", err)
	}

	log := logger.Init(logger.Options{
		Level:     cfg.Logger.Level,
		Format:    logger.Format(cfg.Logger.Format),
		AddSource: cfg.Logger.AddSource,
	})

	connMgr, err := connection.NewManager(connection.Options{
		Logger:  log,
		DataDir: cfg.App.DataDir,
	})
	if err != nil {
		return nil, fmt.Errorf("connection manager: %w", err)
	}

	plugMgr := plugin.NewManager(plugin.Options{
		Logger:  log,
		DataDir: cfg.App.DataDir,
	})

	engine := command.New(command.Options{
		Manager: plugMgr,
		Logger:  log,
		Audit:   command.NewLoggerAudit(log),
		// CLI 场景走 TTY 确认；非交互场景可通过 --yes 走 AllowConfirmer。
		Confirm: ttyConfirmer{},
	})

	return &App{
		Cfg:     cfg,
		Log:     log,
		ConnMgr: connMgr,
		PlugMgr: plugMgr,
		Engine:  engine,
	}, nil
}

// Close 释放已加载插件与后台 goroutine。
// 先让 PluginManager 走一遍 Shutdown（优雅关闭插件内部资源），
// 再 Kill 子进程 —— 顺序颠倒会让插件的 gRPC 连接先断，Shutdown 必然失败。
func (a *App) Close(ctx context.Context) {
	_ = a.PlugMgr.Shutdown(ctx)
	for _, c := range a.loaded {
		c()
	}
}

// -----------------------------------------------------------------------------
// 插件加载：按 pluginID 从 PluginsDir 找可执行文件并注册
// -----------------------------------------------------------------------------

// ensurePluginEnabled 保证指定插件已注册并 Enable。
// 查找规则：<PluginsDir>/<id>[.exe]
func (a *App) ensurePluginEnabled(ctx context.Context, id string) error {
	if e, ok := a.PlugMgr.Get(id); ok {
		if e.State == plugin.StateEnabled {
			return nil
		}
	}
	binPath := filepath.Join(a.Cfg.App.PluginsDir, id+execSuffix())
	if _, err := os.Stat(binPath); err != nil {
		return fmt.Errorf("plugin binary not found: %s (%w)", binPath, err)
	}

	lp, err := loadPluginBinary(binPath, a.Log)
	if err != nil {
		return fmt.Errorf("load plugin %q: %w", id, err)
	}
	a.loaded = append(a.loaded, lp.Close)

	if err := a.PlugMgr.Register(lp.Plugin); err != nil {
		return fmt.Errorf("register plugin %q: %w", id, err)
	}
	pcfg := a.Cfg.Plugins[id]
	if err := a.PlugMgr.Enable(ctx, id, pluginInitRequest(pcfg, a.Cfg)); err != nil {
		return fmt.Errorf("enable plugin %q: %w", id, err)
	}
	return nil
}

func execSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
