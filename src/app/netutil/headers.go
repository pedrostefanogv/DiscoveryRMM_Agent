package netutil

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var agentIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// NormalizeAgentToken validates and normalizes the canonical agent token format.
func NormalizeAgentToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("authToken ausente")
	}
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return "", fmt.Errorf("authToken invalido: informe apenas o token (sem prefixo Bearer)")
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("authToken invalido: token contem espacos")
	}
	if !strings.HasPrefix(token, "mdz_") {
		return "", fmt.Errorf("authToken invalido: esperado prefixo mdz_")
	}
	return token, nil
}

// NormalizeAgentID validates and normalizes the canonical X-Agent-ID value.
func NormalizeAgentID(agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "", fmt.Errorf("X-Agent-ID ausente")
	}
	if !agentIDPattern.MatchString(agentID) {
		return "", fmt.Errorf("X-Agent-ID invalido: esperado GUID")
	}
	return agentID, nil
}

// SetAgentAuthHeaders applies auth headers used by the API.
func SetAgentAuthHeaders(req *http.Request, token string) error {
	normalizedToken, err := NormalizeAgentToken(token)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+normalizedToken)
	return nil
}

// SetAgentAuthHeadersWithAgentID applies auth headers plus X-Agent-ID when available.
func SetAgentAuthHeadersWithAgentID(req *http.Request, token, agentID string) error {
	if err := SetAgentAuthHeaders(req, token); err != nil {
		return err
	}
	normalizedAgentID, err := NormalizeAgentID(agentID)
	if err != nil {
		return err
	}
	req.Header.Set("X-Agent-ID", normalizedAgentID)
	return nil
}
