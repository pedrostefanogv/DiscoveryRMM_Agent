package netutil

import (
	"net/http"
	"strings"
)

// SetAgentAuthHeaders applies auth headers used by the API.
func SetAgentAuthHeaders(req *http.Request, token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	req.Header.Set("X-Agent-Token", token)
	req.Header.Set("Authorization", "Bearer "+token)
}
