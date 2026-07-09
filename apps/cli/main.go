// Command mow 是 MOW 的命令行入口。
//
// v0.1 骨架：加载配置 → 初始化 Logger → 构造 PluginManager → 列出插件。
// Cobra 命令树将在 Command Engine 就绪后接入。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

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

	// TODO(cli): 接入 Cobra，注册 command/list/enable 等子命令。
	// 目前占位打印，验证 Core 可运行。
	ctx := context.Background()
	_ = ctx
	log.Info("plugin manager ready", "plugins", mgr.List())
}
