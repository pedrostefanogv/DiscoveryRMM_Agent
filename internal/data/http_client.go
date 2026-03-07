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

// CatalogCache interface for persistent catalog storage
type CatalogCache interface {
	CacheGetJSON(key string, target interface{}) (bool, error)
	CacheSetJSON(key string, obj interface{}, ttl time.Duration) error
}

// HTTPClient fetches the remote catalog with ETag/Last-Modified caching
// and a body size limit.
type HTTPClient struct {
	url    string
	client *http.Client
	db     CatalogCache

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

// SetDatabase configura cache persistente para o catálogo
func (c *HTTPClient) SetDatabase(db CatalogCache) {
	c.db = db
}

func (c *HTTPClient) GetCatalog(ctx context.Context) (models.Catalog, error) {
	// 1. Tentar cache em memória primeiro (mais rápido)
	c.mu.RLock()
	if c.cachedData != nil {
		defer c.mu.RUnlock()
		return *c.cachedData, nil
	}
	c.mu.RUnlock()

	// 2. Tentar cache persistente no SQLite
	if c.db != nil {
		var cached models.Catalog
		found, err := c.db.CacheGetJSON("winget_catalog", &cached)
		if err == nil && found {
			// Carregar em memória também
			c.mu.Lock()
			c.cachedData = &cached
			c.mu.Unlock()
			return cached, nil
		}
	}

	// 3. Baixar do servidor remoto
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return models.Catalog{}, fmt.Errorf("erro ao montar requisicao: %w", err)
	}

	// Add conditional headers from cache.
	c.mu.RLock()
	etag := c.cachedETag
	lm := c.cachedLM
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
		c.mu.RLock()
		hasCached := c.cachedData != nil
		c.mu.RUnlock()
		if hasCached {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return *c.cachedData, nil
		}
		return models.Catalog{}, fmt.Errorf("erro ao baixar catalogo: %w", err)
	}
	defer resp.Body.Close()

	// 304 Not Modified — return cached copy.
	if resp.StatusCode == http.StatusNotModified {
		c.mu.RLock()
		hasCached := c.cachedData != nil
		c.mu.RUnlock()
		if hasCached {
			c.mu.RLock()
			defer c.mu.RUnlock()
			return *c.cachedData, nil
		}
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

	// Update cache in memory
	c.mu.Lock()
	c.cachedData = &catalog
	c.cachedETag = resp.Header.Get("ETag")
	c.cachedLM = resp.Header.Get("Last-Modified")
	c.cachedAt = time.Now()
	c.mu.Unlock()

	// Update cache in SQLite (24h TTL)
	if c.db != nil {
		if err := c.db.CacheSetJSON("winget_catalog", catalog, 24*time.Hour); err != nil {
			// Log error but don't fail the request
			// (o app.go pode logar isso)
		}
	}

	return catalog, nil
}
