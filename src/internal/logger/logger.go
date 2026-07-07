package logger

import (
	"context"
	"fmt"
	"io"
	"log"
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

// RedirectStdLog captures all stdlib log output and routes it through slog.
// Messages are logged at the specified level. Call this early to ensure
// no log calls are missed by the structured logger.
func RedirectStdLog(level slog.Level) {
	log.SetOutput(&slogWriter{level: level})
	log.SetFlags(0) // slog handles timestamps
}

// slogWriter adapts stdlib log writes into slog calls.
type slogWriter struct {
	level slog.Level
}

func (w *slogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}
	switch w.level {
	case LevelDebug:
		defaultLogger.Debug(msg)
	case LevelInfo:
		defaultLogger.Info(msg)
	case LevelWarn:
		defaultLogger.Warn(msg)
	case LevelError:
		defaultLogger.Error(msg)
	default:
		defaultLogger.Info(msg)
	}
	return len(p), nil
}

// LogBufferAdapter creates a SetSink-compatible callback that writes
// formatted log lines to an append func (e.g. logBuffer.append).
// The format is: [LEVEL] message key=value ...
func LogBufferAdapter(appendLine func(string)) func(slog.Level, string, ...any) {
	return func(level slog.Level, msg string, args ...any) {
		var b strings.Builder
		b.WriteString("[")
		b.WriteString(levelLabel(level))
		b.WriteString("] ")
		b.WriteString(msg)
		for i := 0; i < len(args)-1; i += 2 {
			k, okKey := args[i].(string)
			v := args[i+1]
			if !okKey {
				continue
			}
			b.WriteByte(' ')
			b.WriteString(k)
			b.WriteByte('=')
			if str, ok := v.(string); ok {
				b.WriteString(str)
			} else if err, ok := v.(error); ok {
				b.WriteString(err.Error())
			} else {
				b.WriteString(fmt.Sprint(v))
			}
		}
		appendLine(b.String())
	}
}

func fmtSprint(v any) string {
	if v == nil {
		return "<nil>"
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func levelLabel(l slog.Level) string {
	switch {
	case l < LevelDebug:
		return "TRACE"
	case l < LevelInfo:
		return "DEBUG"
	case l < LevelWarn:
		return "INFO"
	case l < LevelError:
		return "WARN"
	default:
		return "ERROR"
	}
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
