package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	coreai "github.com/mow/mow/core/ai"
	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/connection"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
	"github.com/mow/mow/core/recipe"
	"github.com/mow/mow/core/workflow/history"
	"github.com/mow/mow/sdk"
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
	Recipes *recipe.Registry
	Runner  *recipe.Runner
	History *history.JSONLStore

	// aiOnce 保护 orchestrator 的懒加载：ensurePluginEnabled("ai") 完成后
	// 才能装配 orchestrator（它需要向 Engine 查询工具 Spec）。
	aiOnce sync.Once
	aiOrch *coreai.Orchestrator
	aiErr  error

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
		Manager:  plugMgr,
		Logger:   log,
		Audit:    command.NewLoggerAudit(log),
		Resolver: connMgr, // Engine 通过它把 TargetID → sdk.Connection
		// CLI 场景走 TTY 确认；非交互场景可通过 --yes 走 AllowConfirmer。
		Confirm: ttyConfirmer{},
	})

	return &App{
		Cfg:     cfg,
		Log:     log,
		ConnMgr: connMgr,
		PlugMgr: plugMgr,
		Engine:  engine,
		Recipes: recipe.NewRegistry(),
		Runner:  recipe.NewRunner(engine),
		History: mustHistoryStore(log, cfg.App.DataDir),
	}, nil
}

// mustHistoryStore 构造 JSONL 存储。失败时降级为 nil（Runner 会自然禁用历史）。
// 这样即使 data_dir 权限异常，也不至于让 CLI 无法启动。
func mustHistoryStore(log *logger.Logger, dir string) *history.JSONLStore {
	s, err := history.NewJSONLStore(dir)
	if err != nil {
		log.WithComponent("workflow.history").
			Warn("disable workflow history", "err", err.Error())
		return nil
	}
	return s
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
// 查找规则：优先 <PluginsDir>/<id>/plugin.json 包；v0.5.x 兼容旧的
// <PluginsDir>/<id>[.exe] 平铺二进制。
func (a *App) ensurePluginEnabled(ctx context.Context, id string) error {
	if e, ok := a.PlugMgr.Get(id); ok {
		if e.State == plugin.StateEnabled {
			return nil
		}
	}
	lp, _, legacy, err := plugin.LoadInstalled(a.Cfg.App.PluginsDir, id, &plugin.ManifestGate{Logger: adaptHclog(a.Log)})
	if err != nil {
		return fmt.Errorf("load plugin %q: %w", id, err)
	}
	if legacy {
		a.Log.WithComponent("plugin.loader").Warn("legacy flat plugin layout is deprecated", "id", id)
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

// -----------------------------------------------------------------------------
// AI Orchestrator（v0.4）
// -----------------------------------------------------------------------------

// Orchestrator 懒加载 host-side AI 编排器；已存在则复用。
//
// 装配内容：
//   - Runner：适配 Engine.Run + Engine.Spec 到 coreai.CommandRunner
//   - AllowedTools：来自 Cfg.AI.AllowedTools（空则纯对话，无 tool-use）
//   - Redactor：command.RedactParams（配合 InputSchema 的 x-mow-sensitive）
//   - Auditor：coreai.NewSlogAuditor(a.Log)，事件写结构化日志
//   - 各上限 / 超时：Cfg.AI.MaxXxx；0 走 core/ai 默认
//
// 调用方必须先 ensurePluginEnabled(ctx, "ai") 保证 ai 插件已启用。
func (a *App) Orchestrator() (*coreai.Orchestrator, error) {
	a.aiOnce.Do(func() {
		a.aiOrch, a.aiErr = coreai.New(coreai.Options{
			Runner:           engineRunner{engine: a.Engine},
			AllowedTools:     a.Cfg.AI.AllowedTools,
			MaxRounds:        a.Cfg.AI.MaxRounds,
			MaxCallsPerRound: a.Cfg.AI.MaxCallsPerRound,
			MaxTotalCalls:    a.Cfg.AI.MaxTotalCalls,
			MaxResultBytes:   a.Cfg.AI.MaxResultBytes,
			Timeout:          time.Duration(a.Cfg.AI.TimeoutSeconds) * time.Second,
			Redactor:         command.RedactParams,
			Auditor:          coreai.NewSlogAuditor(a.Log),
		})
	})
	return a.aiOrch, a.aiErr
}

// engineRunner 把 *command.Engine 适配到 coreai.CommandRunner 接口。
// core/command.Engine 天生就有 Run / Spec 两个方法，签名完全匹配，此处只做值包装。
type engineRunner struct{ engine *command.Engine }

func (e engineRunner) Run(ctx context.Context, req command.Request) (*command.Response, error) {
	return e.engine.Run(ctx, req)
}
func (e engineRunner) Spec(pluginID, commandID string) (sdk.CommandSpec, error) {
	return e.engine.Spec(pluginID, commandID)
}
