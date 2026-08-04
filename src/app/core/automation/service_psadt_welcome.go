package automation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func resolvePSADTWelcomeOptions(task AutomationTask) psadtWelcomeOptions {
	options := psadtWelcomeOptions{
		AllowDefer:       true,
		DeferTimes:       defaultDeferTimes,
		DeferRunInterval: defaultDeferInterval,
	}

	applyPayload := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return
		}
		candidate := m
		if nested, ok := m["psadtWelcome"].(map[string]any); ok {
			candidate = nested
		} else if nested, ok := m["welcome"].(map[string]any); ok {
			candidate = nested
		}

		options.AllowDefer = getBoolFromAny(candidate, "allowDefer", options.AllowDefer)
		options.AllowDeferCloseProcesses = getBoolFromAny(candidate, "allowDeferCloseProcesses", options.AllowDeferCloseProcesses)
		options.DeferTimes = getIntFromAny(candidate, "deferTimes", options.DeferTimes)
		options.DeferDays = getFloat64FromAny(candidate, "deferDays", options.DeferDays)
		options.DeferRunInterval = getDurationFromAny(candidate, "deferRunIntervalSeconds", options.DeferRunInterval)
		if deadline := getTimeFromAny(candidate, "deferDeadline"); !deadline.IsZero() {
			options.DeferDeadline = deadline
		}
		options.ForceCountdownSeconds = getIntFromAny(candidate, "forceCountdownSeconds", options.ForceCountdownSeconds)
		options.CloseProcessesCountdownSeconds = getIntFromAny(candidate, "closeProcessesCountdownSeconds", options.CloseProcessesCountdownSeconds)
		options.ForceCloseProcessesCountdown = getIntFromAny(candidate, "forceCloseProcessesCountdown", options.ForceCloseProcessesCountdown)
		options.BlockExecution = getBoolFromAny(candidate, "blockExecution", options.BlockExecution)
		options.CheckDiskSpace = getBoolFromAny(candidate, "checkDiskSpace", options.CheckDiskSpace)
		options.RequiredDiskSpaceMB = getIntFromAny(candidate, "requiredDiskSpaceMb", options.RequiredDiskSpaceMB)
		if list := getStringSliceFromAny(candidate, "closeProcesses"); len(list) > 0 {
			options.CloseProcesses = list
		}
	}

	applyPayload(task.CommandPayload)
	if task.Script != nil {
		applyPayload(task.Script.MetadataJSON)
	}

	if options.DeferTimes <= 0 {
		options.DeferTimes = defaultDeferTimes
	}
	if options.DeferRunInterval <= 0 {
		options.DeferRunInterval = defaultDeferInterval
	}

	return options
}

func getBoolFromAny(m map[string]any, key string, fallback bool) bool {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		t := strings.ToLower(strings.TrimSpace(x))
		return t == "1" || t == "true" || t == "yes" || t == "sim"
	case float64:
		return x != 0
	case int:
		return x != 0
	default:
		return fallback
	}
}

func getIntFromAny(m map[string]any, key string, fallback int) int {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

func getFloat64FromAny(m map[string]any, key string, fallback float64) float64 {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		var n float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%f", &n); err == nil {
			return n
		}
	}
	return fallback
}

func getDurationFromAny(m map[string]any, key string, fallback time.Duration) time.Duration {
	v, ok := m[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case float64:
		if x <= 0 {
			return fallback
		}
		return time.Duration(x) * time.Second
	case int:
		if x <= 0 {
			return fallback
		}
		return time.Duration(x) * time.Second
	case string:
		t := strings.TrimSpace(x)
		if t == "" {
			return fallback
		}
		if d, err := time.ParseDuration(t); err == nil && d > 0 {
			return d
		}
		var sec int
		if _, err := fmt.Sscanf(t, "%d", &sec); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	return fallback
}

func getTimeFromAny(m map[string]any, key string) time.Time {
	v, ok := m[key]
	if !ok {
		return time.Time{}
	}
	t := strings.TrimSpace(fmt.Sprint(v))
	if t == "" {
		return time.Time{}
	}
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05Z07:00", "2006-01-02"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, t); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func getStringSliceFromAny(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	out := make([]string, 0)
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			t := strings.TrimSpace(fmt.Sprint(item))
			if t != "" {
				out = append(out, t)
			}
		}
	case []string:
		for _, item := range x {
			t := strings.TrimSpace(item)
			if t != "" {
				out = append(out, t)
			}
		}
	case string:
		parts := strings.Split(x, ",")
		for _, p := range parts {
			t := strings.TrimSpace(p)
			if t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}
