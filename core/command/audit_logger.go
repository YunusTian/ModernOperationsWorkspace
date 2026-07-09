package command

import (
	"context"

	"github.com/mow/mow/core/logger"
	"github.com/mow/mow/sdk"
)

// LoggerAudit 是把审计事件写入结构化日志的 AuditSink 实现。
//
// 事件字段严格对齐 docs/observability.md：
//
//	audit_id / plugin / command / permission / caller / duration_ms / ok / err
//
// SQLite append-only 存储版本将在 v0.2 引入 (core/audit 包)，
// 届时把 LoggerAudit 换成 SQLiteAudit 即可，Engine 无需修改。
type LoggerAudit struct {
	log *logger.Logger
}

// NewLoggerAudit 返回一个以指定 Logger 输出审计事件的 Sink。
// 若 log 为 nil，使用 logger.Default()。
func NewLoggerAudit(log *logger.Logger) *LoggerAudit {
	if log == nil {
		log = logger.Default()
	}
	return &LoggerAudit{log: log.WithComponent("audit")}
}

func (a *LoggerAudit) Start(ctx context.Context, rec *AuditRecord) {
	a.log.Info("command start",
		"audit_id", rec.AuditID,
		"plugin", rec.PluginID,
		"command", rec.CommandID,
		"permission", rec.Permission.String(),
		"caller", callerString(rec.Caller),
		"streaming", rec.Streaming,
		"confirmed", rec.Confirmed,
		"params_size", len(rec.Params),
	)
}

func (a *LoggerAudit) Finish(ctx context.Context, rec *AuditRecord) {
	fields := []any{
		"audit_id", rec.AuditID,
		"plugin", rec.PluginID,
		"command", rec.CommandID,
		"duration_ms", rec.Duration.Milliseconds(),
		"ok", rec.Err == nil,
	}
	if rec.Err != nil {
		fields = append(fields, "err", rec.Err.Error())
	}
	if rec.Err == nil {
		a.log.Info("command finish", fields...)
	} else {
		a.log.Warn("command finish", fields...)
	}
}

func callerString(c sdk.Caller) string {
	kind := callerKindString(c.Type)
	if c.User == "" {
		return kind
	}
	return kind + ":" + c.User
}

func callerKindString(t sdk.CallerType) string {
	switch t {
	case sdk.CallerCLI:
		return "cli"
	case sdk.CallerDesktop:
		return "desktop"
	case sdk.CallerAPI:
		return "api"
	case sdk.CallerAI:
		return "ai"
	case sdk.CallerWorkflow:
		return "workflow"
	case sdk.CallerRecipe:
		return "recipe"
	default:
		return "unknown"
	}
}
