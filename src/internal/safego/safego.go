// Package safego provides panic-safe goroutine launching to prevent
// the entire application from crashing when a goroutine panics.
// It captures the stack trace and logs it.
package safego

import (
	"fmt"
	"log"
	"runtime"
)

// LogFunc is called with log lines when a goroutine panics.
// The first call receives the panic header, subsequent calls receive stack trace lines.
type LogFunc func(line string)

// Go runs fn in a new goroutine with panic recovery.
// If fn panics, the panic value and full stack trace are passed to logf.
func Go(fn func(), logf LogFunc) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				stack := make([]byte, 65536)
				n := runtime.Stack(stack, false)
				if logf != nil {
					logf(fmt.Sprintf("[PANIC] goroutine panicou: %v", r))
					logf("[PANIC] stack trace:")
					for _, line := range splitLines(string(stack[:n])) {
						logf(line)
					}
				} else {
					// Fallback: sem callback de log, usa log.Printf padrao
					log.Printf("[PANIC] goroutine panicou: %v\n%s", r, string(stack[:n]))
				}
			}
		}()
		fn()
	}()
}

func splitLines(raw string) []string {
	parts := make([]string, 0)
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			parts = append(parts, raw[start:i])
			start = i + 1
		}
	}
	if start < len(raw) {
		parts = append(parts, raw[start:])
	}
	return parts
}
