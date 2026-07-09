// Command mow 是 MOW 的命令行入口。
//
// v0.1 骨架：
//   Config → Logger → PluginManager → Command Engine → 打印可用 Command。
// Cobra 命令树将在后续接入。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/core/plugin"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to mow config (JSON); empty = defaults")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config:", err)
		os.Exit(1)
	}

	log := logger.Init(logger.Options{
		Level:     cfg.Logger.Level,
		Format:    logger.Format(cfg.Logger.Format),
		AddSource: cfg.Logger.AddSource,
	})
	log.Info("mow starting",
		"data_dir", cfg.App.DataDir,
		"plugins_dir", cfg.App.PluginsDir,
	)

	mgr := plugin.NewManager(plugin.Options{
		Logger:  log,
		DataDir: cfg.App.DataDir,
	})

	// v0.1：CLI 场景下危险操作默认拒绝；后续接入 TTY prompt 时替换。
	engine := command.New(command.Options{
		Manager: mgr,
		Logger:  log,
		Audit:   command.NewLoggerAudit(log),
		Confirm: command.DenyConfirmer{},
	})

	ctx := context.Background()
	log.Info("engine ready",
		"plugins", mgr.List(),
	)

	// TODO(cli): 接入 Cobra，注册 command/list/enable/run 等子命令；
	// 所有子命令最终都调用 engine.Run / engine.RunStream。
	_ = engine
	_ = ctx
}
