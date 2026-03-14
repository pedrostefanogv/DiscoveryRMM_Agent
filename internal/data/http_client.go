package data

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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

type catalogMeta struct {
	ETag         string `json:"etag"`
	LastModified string `json:"lastModified"`
}

const (
	catalogDataCacheKey = "winget_catalog"
	catalogMetaCacheKey = "winget_catalog_meta"
)

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
		cached := *c.cachedData
		etag := c.cachedETag
		lm := c.cachedLM
		c.mu.RUnlock()

		if fresh, ok := c.tryRevalidateRemote(ctx, etag, lm); ok {
			return fresh, nil
		}
		return cached, nil
	}
	c.mu.RUnlock()

	// 2. Tentar cache persistente no SQLite
	if c.db != nil {
		var cached models.Catalog
		found, err := c.db.CacheGetJSON(catalogDataCacheKey, &cached)
		if err == nil && found {
			var meta catalogMeta
			if ok, metaErr := c.db.CacheGetJSON(catalogMetaCacheKey, &meta); metaErr != nil || !ok {
				meta = catalogMeta{}
			}

			// Carregar em memória também
			c.mu.Lock()
			c.cachedData = &cached
			c.cachedETag = strings.TrimSpace(meta.ETag)
			c.cachedLM = strings.TrimSpace(meta.LastModified)
			c.cachedAt = time.Now()
			c.mu.Unlock()

			if fresh, ok := c.tryRevalidateRemote(ctx, strings.TrimSpace(meta.ETag), strings.TrimSpace(meta.LastModified)); ok {
				return fresh, nil
			}
			return cached, nil
		}
	}

	// 3. Baixar do servidor remoto
	fresh, err := c.fetchRemoteCatalog(ctx, "", "")
	if err == nil {
		return fresh, nil
	}

	// On network/error, return cache if available.
	c.mu.RLock()
	hasCached := c.cachedData != nil
	if hasCached {
		cached := *c.cachedData
		c.mu.RUnlock()
		return cached, nil
	}
	c.mu.RUnlock()

	return models.Catalog{}, err
}

func (c *HTTPClient) tryRevalidateRemote(ctx context.Context, etag, lm string) (models.Catalog, bool) {
	fresh, err := c.fetchRemoteCatalog(ctx, etag, lm)
	if err != nil {
		return models.Catalog{}, false
	}
	return fresh, true
}

func (c *HTTPClient) fetchRemoteCatalog(ctx context.Context, etag, lm string) (models.Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return models.Catalog{}, fmt.Errorf("erro ao montar requisicao: %w", err)
	}

	if strings.TrimSpace(etag) != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if strings.TrimSpace(lm) != "" {
		req.Header.Set("If-Modified-Since", lm)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		// On network error, return cache if available.
		c.mu.RLock()
		hasCached := c.cachedData != nil
		if hasCached {
			cached := *c.cachedData
			c.mu.RUnlock()
			return cached, nil
		}
		c.mu.RUnlock()
		return models.Catalog{}, fmt.Errorf("erro ao baixar catalogo: %w", err)
	}
	defer resp.Body.Close()

	// 304 Not Modified — return cached copy.
	if resp.StatusCode == http.StatusNotModified {
		c.mu.RLock()
		if c.cachedData != nil {
			cached := *c.cachedData
			c.mu.RUnlock()
			return cached, nil
		}
		c.mu.RUnlock()
		return models.Catalog{}, fmt.Errorf("catalogo retornou 304 sem cache local")
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
		if err := c.db.CacheSetJSON(catalogDataCacheKey, catalog, 24*time.Hour); err != nil {
			// Log error but don't fail the request
			// (o app.go pode logar isso)
		}
		_ = c.db.CacheSetJSON(catalogMetaCacheKey, catalogMeta{
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
		}, 24*time.Hour)
	}

	return catalog, nil
}
