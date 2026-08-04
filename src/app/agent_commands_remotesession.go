package app

import (
	"encoding/json"
	"strings"
)

// ── Remote Session Helpers ──

func isRemoteSessionCommandType(cmdType string) bool {
	switch strings.ToLower(strings.TrimSpace(cmdType)) {
	case "remotesessionstart", "remote_session_start", "remotesessionstop", "remote_session_stop",
		"remotesessionquality", "remote_session_quality",
		"recordingstart", "recording_start", "recordingstop", "recording_stop":
		return true
	default:
		return false
	}
}

func parseAnyMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	// Trata double-encoding: payload chegou como string JSON (ex: via Relay que
	// re-encoda o payload). Sem isso, o Manager.HandleCommand recebe nil e retorna
	// handled=false, fazendo o fallback executar o JSON como comando shell.
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if len(s) > 0 && s[0] == '{' {
			var m map[string]any
			if err := json.Unmarshal([]byte(s), &m); err == nil {
				return m
			}
		}
	}
	return nil
}
