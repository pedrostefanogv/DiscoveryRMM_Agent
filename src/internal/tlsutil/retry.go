package tlsutil

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// ── HTTP Retry RoundTripper ────────────────────────────────────────────────

const (
	defaultMaxRetries  = 3
	defaultBaseBackoff = 1 * time.Second
	defaultMaxBackoff  = 30 * time.Second
)

// RetryConfig define a política de retry para HTTP requests.
type RetryConfig struct {
	MaxRetries  int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

// DefaultRetryConfig retorna a configuração padrão de retry.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:  defaultMaxRetries,
		BaseBackoff: defaultBaseBackoff,
		MaxBackoff:  defaultMaxBackoff,
	}
}

// NewHTTPClientWithRetry cria um http.Client com retry automático via RoundTripper wrapper.
func NewHTTPClientWithRetry(timeout time.Duration, retryCfg RetryConfig) *http.Client {
	if retryCfg.MaxRetries <= 0 {
		retryCfg = DefaultRetryConfig()
	}
	client := NewHTTPClient(timeout)
	client.Transport = &retryRoundTripper{
		base: client.Transport,
		cfg:  retryCfg,
	}
	return client
}

// NewHTTPClient com retry padrão.
func NewDefaultRetryHTTPClient(timeout time.Duration) *http.Client {
	return NewHTTPClientWithRetry(timeout, DefaultRetryConfig())
}

type retryRoundTripper struct {
	base http.RoundTripper
	cfg  RetryConfig
}

func (rt *retryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Save body for retry if needed
	var bodyBytes []byte
	if req.Body != nil && req.Body != http.NoBody {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	var lastErr error
	for attempt := 0; attempt <= rt.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			// Re-create body reader for retry
			if len(bodyBytes) > 0 {
				req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			}
			backoff := backoffDuration(attempt, rt.cfg.BaseBackoff, rt.cfg.MaxBackoff)
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(backoff):
			}
		}

		resp, err := rt.base.RoundTrip(req)
		if err != nil {
			lastErr = err
			if !isRetryableError(err) {
				return nil, err
			}
			continue
		}

		// Retry on server errors (5xx) and 429 Too Many Requests
		if resp.StatusCode >= 500 || resp.StatusCode == 429 {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}

		return resp, nil
	}

	return nil, fmt.Errorf("retry exhausted after %d attempts: %w", rt.cfg.MaxRetries+1, lastErr)
}

func backoffDuration(attempt int, base, max time.Duration) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempt-1))) * base
	if d > max {
		d = max
	}
	return d
}

func isRetryableError(err error) bool {
	// Connection errors are always retryable
	return err != nil
}
