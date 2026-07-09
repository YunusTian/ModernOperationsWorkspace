// Package logger 提供 MOW 的结构化日志入口。
//
// v0.1 基于 log/slog（Go 标准库），零第三方依赖：
//   - 结构化 JSON（生产环境）或人类可读文本（开发环境）
//   - 分级：DEBUG / INFO / WARN / ERROR
//   - 全局 default logger 与派生子 logger（WithComponent / WithFields）
//
// 详见 docs/observability.md。
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// Format 定义日志输出格式。
type Format string

const (
	FormatJSON Format = "json"
	FormatText Format = "text"
)

// Options 是 Logger 初始化参数。
type Options struct {
	// Level 日志级别："debug" / "info" / "warn" / "error"，大小写不敏感。
	Level string

	// Format 输出格式：json / text。默认 json。
	Format Format

	// Output 日志输出目标；nil 时使用 os.Stderr。
	Output io.Writer

	// AddSource 是否附加调用位置（性能开销较高，默认 false）。
	AddSource bool
}

// Logger 是 MOW 对外统一的日志接口，
// 在 slog.Logger 之上做了 With / Component / Sub 的语义收敛。
type Logger struct {
	inner *slog.Logger
}

var (
	defaultMu sync.RWMutex
	// 保底 default，在 Init 之前使用也不会 panic。
	defaultLogger = &Logger{inner: slog.New(slog.NewTextHandler(os.Stderr, nil))}
)

// Init 初始化全局 Logger 并返回。多次调用会覆盖上一次。
func Init(opts Options) *Logger {
	if opts.Output == nil {
		opts.Output = os.Stderr
	}
	if opts.Format == "" {
		opts.Format = FormatJSON
	}

	handlerOpts := &slog.HandlerOptions{
		Level:     parseLevel(opts.Level),
		AddSource: opts.AddSource,
	}

	var h slog.Handler
	switch opts.Format {
	case FormatText:
		h = slog.NewTextHandler(opts.Output, handlerOpts)
	default:
		h = slog.NewJSONHandler(opts.Output, handlerOpts)
	}

	l := &Logger{inner: slog.New(h)}
	defaultMu.Lock()
	defaultLogger = l
	defaultMu.Unlock()
	return l
}

// Default 返回全局 Logger。
func Default() *Logger {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultLogger
}

// WithComponent 派生一个带 component 字段的子 Logger。
//
//	logger.Default().WithComponent("plugin.manager").Info("loaded")
func (l *Logger) WithComponent(name string) *Logger {
	return &Logger{inner: l.inner.With("component", name)}
}

// WithFields 附加任意 key-value（键值对，交替出现）。
//
//	logger.WithFields("plugin", "ssh", "version", "0.1.0")
func (l *Logger) WithFields(kv ...any) *Logger {
	return &Logger{inner: l.inner.With(kv...)}
}

func (l *Logger) Debug(msg string, kv ...any) { l.inner.Debug(msg, kv...) }
func (l *Logger) Info(msg string, kv ...any)  { l.inner.Info(msg, kv...) }
func (l *Logger) Warn(msg string, kv ...any)  { l.inner.Warn(msg, kv...) }
func (l *Logger) Error(msg string, kv ...any) { l.inner.Error(msg, kv...) }

// Slog 暴露底层 slog.Logger，便于直接对接第三方库。
func (l *Logger) Slog() *slog.Logger { return l.inner }

// -----------------------------------------------------------------------------
// context 集成
// -----------------------------------------------------------------------------

type ctxKey struct{}

// Into 把 Logger 注入到 context 中。
func Into(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From 从 context 中取 Logger，若不存在则回退到 Default。
func From(ctx context.Context) *Logger {
	if v, ok := ctx.Value(ctxKey{}).(*Logger); ok && v != nil {
		return v
	}
	return Default()
}

// -----------------------------------------------------------------------------
// 内部工具
// -----------------------------------------------------------------------------

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
