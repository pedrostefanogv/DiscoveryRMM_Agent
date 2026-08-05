package p2p

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIErrorEnvelope representa o envelope de erro retornado pela API.
type APIErrorEnvelope struct {
	Error             string `json:"error"`
	Field             string `json:"field,omitempty"`
	Code              string `json:"code,omitempty"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
}

// DistributionStatusQueryOptions define filtros opcionais usados por
// GET /api/v1/agent-auth/me/p2p/distribution-status.
type DistributionStatusQueryOptions struct {
	ArtifactID string
	Limit      int
	Offset     int
}

// DistributionStatusEndpointWithOptions anexa filtros/paginação ao endpoint.
func DistributionStatusEndpointWithOptions(endpoint string, opts DistributionStatusQueryOptions) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	if artifactID := strings.TrimSpace(opts.ArtifactID); artifactID != "" {
		q.Set("artifactId", artifactID)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Offset > 0 {
		q.Set("offset", strconv.Itoa(opts.Offset))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// APIResponseError converte um http.Response não-2xx em um erro tipado,
// extraindo o envelope de erro quando disponível.
func APIResponseError(apiName string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	trimmedBody := strings.TrimSpace(string(body))
	if trimmedBody == "" {
		return &APIError{APIName: apiName, StatusCode: resp.StatusCode}
	}
	var parsed APIErrorEnvelope
	if err := json.Unmarshal(body, &parsed); err == nil && strings.TrimSpace(parsed.Error) != "" {
		details := strings.TrimSpace(parsed.Error)
		if code := strings.TrimSpace(parsed.Code); code != "" {
			details += " (code=" + code + ")"
		}
		if field := strings.TrimSpace(parsed.Field); field != "" {
			details += " (field=" + field + ")"
		}
		if parsed.RetryAfterSeconds > 0 {
			details += " (retryAfterSeconds=" + strconv.Itoa(parsed.RetryAfterSeconds) + ")"
		}
		return &APIError{APIName: apiName, StatusCode: resp.StatusCode, Detail: details}
	}
	return &APIError{APIName: apiName, StatusCode: resp.StatusCode, Detail: trimmedBody}
}

// APIError é um erro estruturado de resposta HTTP da API.
type APIError struct {
	APIName    string
	StatusCode int
	Detail     string
}

func (e *APIError) Error() string {
	if strings.TrimSpace(e.Detail) != "" {
		return e.APIName + " HTTP " + strconv.Itoa(e.StatusCode) + ": " + e.Detail
	}
	return e.APIName + " HTTP " + strconv.Itoa(e.StatusCode)
}

// RetryAfterFromResponse extrai o instante de retry a partir do header
// Retry-After ou do envelope JSON. Retorna (zero, false) quando não aplicável.
func RetryAfterFromResponse(resp *http.Response, now time.Time) (time.Time, bool) {
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return now.Add(time.Duration(seconds) * time.Second), true
		}
		if ts, err := http.ParseTime(raw); err == nil {
			if ts.After(now) {
				return ts, true
			}
		}
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > 0 {
		var parsed APIErrorEnvelope
		if err := json.Unmarshal(body, &parsed); err == nil && parsed.RetryAfterSeconds > 0 {
			return now.Add(time.Duration(parsed.RetryAfterSeconds) * time.Second), true
		}
	}
	return time.Time{}, false
}
