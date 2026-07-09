package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	hclog "github.com/hashicorp/go-hclog"

	"github.com/mow/mow/core/command"
	"github.com/mow/mow/core/config"
	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/sdk"
	"github.com/mow/mow/sdk/pluginclient"
)

// -----------------------------------------------------------------------------
// 插件加载封装
// -----------------------------------------------------------------------------

// loadPluginBinary 启动 path 指向的插件子进程。
func loadPluginBinary(path string, log *logger.Logger) (*pluginclient.LoadedPlugin, error) {
	return pluginclient.LoadFromBinary(path, adaptHclog(log))
}

// pluginInitRequest 构造 sdk.InitRequest。
func pluginInitRequest(pc config.PluginConfig, cfg config.Config) sdk.InitRequest {
	return sdk.InitRequest{
		Settings: pc.Settings,
		DataDir:  filepath.Join(cfg.App.DataDir, "plugin-data"),
	}
}

// adaptHclog 把 MOW Logger 适配成 hclog.Logger。
// v0.1 简化：日志转发给 stderr；未来可写一个真正的 slog<->hclog 适配器。
func adaptHclog(_ *logger.Logger) hclog.Logger {
	return hclog.New(&hclog.LoggerOptions{
		Name:   "mow-plugin",
		Output: os.Stderr,
		Level:  hclog.Warn,
	})
}

// -----------------------------------------------------------------------------
// ttyConfirmer —— CLI 场景下的 Dangerous 二次确认
// -----------------------------------------------------------------------------

// ttyConfirmer 通过 stdin/stdout 与用户交互；非 TTY 时一律拒绝。
type ttyConfirmer struct{}

func (ttyConfirmer) Confirm(_ context.Context, r command.ConfirmationRequest) (bool, error) {
	if !isTerminal(os.Stdin) {
		return false, sdk.ErrConfirmationRequired
	}
	fmt.Fprintf(os.Stdout,
		"\n[DANGEROUS] %s.%s (audit=%s)\nparams: %s\ntype 'yes' to confirm: ",
		r.PluginID, r.CommandID, r.AuditID, string(r.Params),
	)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false, sdk.ErrConfirmationRequired
	}
	return strings.EqualFold(strings.TrimSpace(sc.Text()), "yes"), nil
}
