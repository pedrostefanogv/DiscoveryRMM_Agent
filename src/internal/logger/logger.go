// Package logger fornece logging estruturado baseado em slog para toda a aplicação.
// Centraliza níveis, formatação e saída, substituindo log.Println e concatenação manual.
package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Level representa o nível de severidade (compatível com slog.Level).
type Level = slog.Level

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

var defaultLogger *slog.Logger

func init() {
	defaultLogger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: LevelInfo,
	}))
}

// Default retorna o logger padrão (JSON, stderr, nível Info).
func Default() *slog.Logger { return defaultLogger }

// SetDefault substitui o logger padrão.
func SetDefault(l *slog.Logger) { defaultLogger = l }

// SetLevel ajusta o nível do logger padrão dinamicamente.
func SetLevel(level Level) {
	defaultLogger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	}))
}

// SetFileOutput redireciona o logger padrão para um arquivo com formato texto.
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
	defaultLogger = slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, f), &slog.HandlerOptions{
		Level: LevelInfo,
	}))
	return nil
}

// ─── Helpers de conveniência ────────────────────────────────────────

// Debug emite log nível Debug.
func Debug(msg string, args ...any) {
	defaultLogger.Debug(msg, args...)
}

// Info emite log nível Info.
func Info(msg string, args ...any) {
	defaultLogger.Info(msg, args...)
}

// Warn emite log nível Warn.
func Warn(msg string, args ...any) {
	defaultLogger.Warn(msg, args...)
}

// Error emite log nível Error.
func Error(msg string, args ...any) {
	defaultLogger.Error(msg, args...)
}

// With cria um logger com atributos pré-definidos (ex: componente, agente).
func With(args ...any) *slog.Logger {
	return defaultLogger.With(args...)
}
