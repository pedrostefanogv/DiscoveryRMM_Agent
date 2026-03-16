package data

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"discovery/internal/models"
)

func TestHTTPClient_GetCatalog_Success(t *testing.T) {
	catalog := models.Catalog{
		Count:    2,
		Packages: []models.AppItem{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}},
	}
	body, _ := json.Marshal(catalog)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"test-etag"`)
		w.Write(body)
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, 5*time.Second)
	result, err := client.GetCatalog(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 2 {
		t.Errorf("Count = %d, want 2", result.Count)
	}
	if len(result.Packages) != 2 {
		t.Errorf("Packages len = %d, want 2", len(result.Packages))
	}
}

func TestHTTPClient_GetCatalog_304NotModified(t *testing.T) {
	calls := 0
	catalog := models.Catalog{Count: 1, Packages: []models.AppItem{{ID: "a", Name: "A"}}}
	body, _ := json.Marshal(catalog)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"test-etag"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"test-etag"`)
		w.Write(body)
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, 5*time.Second)

	// First call — should fetch
	_, err := client.GetCatalog(context.Background())
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call — should get 304
	result, err := client.GetCatalog(context.Background())
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if result.Count != 1 {
		t.Errorf("cached Count = %d, want 1", result.Count)
	}
	if calls != 2 {
		t.Errorf("server calls = %d, want 2", calls)
	}
}

func TestHTTPClient_GetCatalog_LoadsFromSQLiteAndRevalidates(t *testing.T) {
	calls := 0
	catalog := models.Catalog{Count: 7, Packages: []models.AppItem{{ID: "cached", Name: "Cached"}}}
	body, _ := json.Marshal(catalog)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") != `"sqlite-etag"` {
			t.Fatalf("expected If-None-Match sqlite-etag, got %q", r.Header.Get("If-None-Match"))
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, 5*time.Second)
	mem := newMemoryCatalogCache()
	_ = mem.CacheSetJSON(catalogDataCacheKey, catalog, 24*time.Hour)
	_ = mem.CacheSetJSON(catalogMetaCacheKey, catalogMeta{ETag: `"sqlite-etag"`}, 24*time.Hour)
	client.SetDatabase(mem)

	result, err := client.GetCatalog(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Count != 7 {
		t.Fatalf("count = %d, want 7", result.Count)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1", calls)
	}
	_ = body
}

type memoryCatalogCache struct {
	items map[string][]byte
}

func newMemoryCatalogCache() *memoryCatalogCache {
	return &memoryCatalogCache{items: map[string][]byte{}}
}

func (m *memoryCatalogCache) CacheGetJSON(key string, target interface{}) (bool, error) {
	raw, ok := m.items[key]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return false, err
	}
	return true, nil
}

func (m *memoryCatalogCache) CacheSetJSON(key string, obj interface{}, _ time.Duration) error {
	raw, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	m.items[key] = raw
	return nil
}

func TestHTTPClient_GetCatalog_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewHTTPClient(srv.URL, 5*time.Second)
	_, err := client.GetCatalog(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestHTTPClient_GetCatalog_NetworkError_ReturnsCached(t *testing.T) {
	catalog := models.Catalog{Count: 3}
	body, _ := json.Marshal(catalog)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))

	client := NewHTTPClient(srv.URL, 5*time.Second)

	// Populate cache
	_, err := client.GetCatalog(context.Background())
	if err != nil {
		t.Fatalf("initial fetch: %v", err)
	}

	// Close server to simulate network error
	srv.Close()

	// Should return cached data
	result, err := client.GetCatalog(context.Background())
	if err != nil {
		t.Fatalf("expected cached result, got error: %v", err)
	}
	if result.Count != 3 {
		t.Errorf("cached Count = %d, want 3", result.Count)
	}
}
