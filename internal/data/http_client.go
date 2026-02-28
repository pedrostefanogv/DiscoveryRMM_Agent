package data

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"winget-store/internal/models"
)

// maxCatalogSize limits the response body to prevent runaway memory usage
// when downloading the remote package catalog. 50 MB is sufficient for the
// current catalog (~25k packages) with generous headroom for growth.
const maxCatalogSize = 50 * 1024 * 1024

// HTTPClient fetches the remote catalog with ETag/Last-Modified caching
// and a body size limit.
type HTTPClient struct {
	url    string
	client *http.Client

	mu         sync.RWMutex
	cachedData *models.Catalog
	cachedETag string
	cachedLM   string // Last-Modified header
	cachedAt   time.Time
}

func NewHTTPClient(url string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		url: url,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (c *HTTPClient) GetCatalog(ctx context.Context) (models.Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return models.Catalog{}, fmt.Errorf("erro ao montar requisicao: %w", err)
	}

	// Add conditional headers from cache.
	c.mu.RLock()
	etag := c.cachedETag
	lm := c.cachedLM
	hasCached := c.cachedData != nil
	c.mu.RUnlock()

	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lm != "" {
		req.Header.Set("If-Modified-Since", lm)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		// On network error, return cache if available.
		if hasCached {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return *c.cachedData, nil
		}
		return models.Catalog{}, fmt.Errorf("erro ao baixar catalogo: %w", err)
	}
	defer resp.Body.Close()

	// 304 Not Modified — return cached copy.
	if resp.StatusCode == http.StatusNotModified && hasCached {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return *c.cachedData, nil
	}

	if resp.StatusCode != http.StatusOK {
		return models.Catalog{}, fmt.Errorf("catalogo respondeu com status %d", resp.StatusCode)
	}

	// Limit body size to prevent runaway memory usage.
	limited := io.LimitReader(resp.Body, maxCatalogSize)
	var catalog models.Catalog
	if err := json.NewDecoder(limited).Decode(&catalog); err != nil {
		return models.Catalog{}, fmt.Errorf("erro ao decodificar catalogo: %w", err)
	}

	// Update cache.
	c.mu.Lock()
	c.cachedData = &catalog
	c.cachedETag = resp.Header.Get("ETag")
	c.cachedLM = resp.Header.Get("Last-Modified")
	c.cachedAt = time.Now()
	c.mu.Unlock()

	return catalog, nil
}
