package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Level = slog.Level

const (
	LevelTrace = slog.Level(-8)
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

var (
	defaultLogger *slog.Logger

	sinkMu  sync.RWMutex
	logSink func(level slog.Level, msg string, args ...any)

	innerMu     sync.RWMutex
	innerConfig innerHandlerConfig
)

type innerHandlerConfig struct {
	handler slog.Handler
	level   Level
}

func init() {
	setupLogger(LevelInfo)
}

func setupLogger(level Level) {
	inner := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	innerMu.Lock()
	innerConfig = innerHandlerConfig{handler: inner, level: level}
	innerMu.Unlock()
	defaultLogger = slog.New(&sinkHandler{})
}

func Default() *slog.Logger { return defaultLogger }

func SetDefault(l *slog.Logger) { defaultLogger = l }

func SetLevel(level Level) {
	innerMu.Lock()
	innerConfig.level = level
	innerConfig.handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	innerMu.Unlock()
}

func SetFileOutput(logPath string) error {
	if strings.TrimSpace(logPath) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	innerMu.Lock()
	innerConfig.level = LevelInfo
	innerConfig.handler = slog.NewTextHandler(io.MultiWriter(os.Stderr, f), &slog.HandlerOptions{
		Level: LevelInfo,
	})
	innerMu.Unlock()
	return nil
}

func SetSink(fn func(level slog.Level, msg string, args ...any)) {
	sinkMu.Lock()
	logSink = fn
	sinkMu.Unlock()
}

type sinkHandler struct{}

func (h *sinkHandler) Enabled(_ context.Context, level slog.Level) bool {
	sinkMu.RLock()
	hasSink := logSink != nil
	sinkMu.RUnlock()
	if hasSink {
		return true
	}
	innerMu.RLock()
	lvl := innerConfig.level
	innerMu.RUnlock()
	return level >= lvl
}

func (h *sinkHandler) Handle(_ context.Context, r slog.Record) error {
	sinkMu.RLock()
	fn := logSink
	sinkMu.RUnlock()
	if fn != nil {
		var args []any
		r.Attrs(func(a slog.Attr) bool {
			args = append(args, a)
			return true
		})
		fn(r.Level, r.Message, args...)
	}

	innerMu.RLock()
	handler := innerConfig.handler
	innerMu.RUnlock()
	return handler.Handle(context.Background(), r)
}

func (h *sinkHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &sinkAttrsHandler{parent: h, attrs: attrs}
}

func (h *sinkHandler) WithGroup(name string) slog.Handler {
	return &sinkGroupHandler{parent: h, group: name}
}

type sinkAttrsHandler struct {
	parent *sinkHandler
	attrs  []slog.Attr
}

func (h *sinkAttrsHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.parent.Enabled(ctx, level)
}

func (h *sinkAttrsHandler) Handle(ctx context.Context, r slog.Record) error {
	sinkMu.RLock()
	fn := logSink
	sinkMu.RUnlock()
	if fn != nil {
		var args []any
		for _, a := range h.attrs {
			args = append(args, a)
		}
		r.Attrs(func(a slog.Attr) bool {
			args = append(args, a)
			return true
		})
		fn(r.Level, r.Message, args...)
	}

	innerMu.RLock()
	handler := innerConfig.handler
	innerMu.RUnlock()
	return handler.WithAttrs(h.attrs).Handle(context.Background(), r)
}

func (h *sinkAttrsHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	combined = append(combined, h.attrs...)
	combined = append(combined, attrs...)
	return &sinkAttrsHandler{parent: h.parent, attrs: combined}
}

func (h *sinkAttrsHandler) WithGroup(name string) slog.Handler {
	return &sinkGroupHandler{parent: h.parent, attrs: h.attrs, group: name}
}

type sinkGroupHandler struct {
	parent *sinkHandler
	attrs  []slog.Attr
	group  string
}

func (h *sinkGroupHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.parent.Enabled(ctx, level)
}

func (h *sinkGroupHandler) Handle(ctx context.Context, r slog.Record) error {
	sinkMu.RLock()
	fn := logSink
	sinkMu.RUnlock()
	if fn != nil {
		var args []any
		for _, a := range h.attrs {
			args = append(args, a)
		}
		r.Attrs(func(a slog.Attr) bool {
			args = append(args, a)
			return true
		})
		fn(r.Level, r.Message, args...)
	}

	innerMu.RLock()
	handler := innerConfig.handler
	innerMu.RUnlock()
	return handler.WithAttrs(h.attrs).WithGroup(h.group).Handle(context.Background(), r)
}

func (h *sinkGroupHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	combined = append(combined, h.attrs...)
	combined = append(combined, attrs...)
	return &sinkGroupHandler{parent: h.parent, attrs: combined, group: h.group}
}

func (h *sinkGroupHandler) WithGroup(name string) slog.Handler {
	return &sinkGroupHandler{parent: h.parent, attrs: h.attrs, group: h.group + "." + name}
}

func Debug(msg string, args ...any) {
	defaultLogger.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	defaultLogger.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	defaultLogger.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	defaultLogger.Error(msg, args...)
}

func With(args ...any) *slog.Logger {
	return defaultLogger.With(args...)
}
