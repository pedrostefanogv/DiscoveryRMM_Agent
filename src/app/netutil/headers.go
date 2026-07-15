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
		return "", fmt.Errorf("authToken inválido: informe apenas o token (sem prefixo Bearer)")
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("authToken inválido: token contém espaços")
	}
	if !strings.HasPrefix(token, "mdz_") {
		return "", fmt.Errorf("authToken inválido: esperado prefixo mdz_")
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
		return "", fmt.Errorf("X-Agent-ID inválido: esperado GUID")
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

// SanitizeHTTPErrorBody limpa o corpo de uma resposta de erro HTTP para exibição
// em logs. Se o corpo for HTML (ex: página de erro do nginx), retorna uma
// mensagem limpa com o código HTTP; caso contrário, retorna o texto truncado.
func SanitizeHTTPErrorBody(statusCode int, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Sprintf("HTTP %d (sem corpo)", statusCode)
	}
	// Detecta HTML retornado por proxies/nginx quando o backend esta fora
	if strings.HasPrefix(body, "<") || strings.Contains(body, "<html>") || strings.Contains(body, "<HTML>") {
		return fmt.Sprintf("HTTP %d (servidor intermediário retornou página de erro)", statusCode)
	}
	// Trunca respostas muito longas (ex.: stack traces ou JSON extenso)
	const maxLen = 300
	if len(body) > maxLen {
		return body[:maxLen] + "..."
	}
	return body
}
