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
	"github.com/mow/mow/core/plugin/settings"
	"github.com/mow/mow/sdk"
)

// -----------------------------------------------------------------------------
// 插件加载封装
// -----------------------------------------------------------------------------

// pluginInitRequest 构造 sdk.InitRequest。v0.5.2 P1 起：如果 <DataDir>/plugin-secrets/<id>.json
// 存在，就在传给插件前把 secret sidecar 合并回 pc.Settings，让插件收到完整数据。
// 合并失败不阻塞启动，只记录一次告警到 stderr（避免日志系统写明文）。
func pluginInitRequest(id string, pc config.PluginConfig, cfg config.Config) sdk.InitRequest {
	merged := pc.Settings
	if cfg.App.DataDir != "" {
		store := settings.NewStoreFromDataDir(cfg.App.DataDir)
		if sec, ok, err := store.Load(id); err == nil && ok {
			if out, mErr := settings.Merge(merged, sec); mErr == nil {
				merged = out
			} else {
				fmt.Fprintf(os.Stderr, "plugin %s: merge secret sidecar: %v\n", id, mErr)
			}
		}
	}
	return sdk.InitRequest{
		Settings: merged,
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
