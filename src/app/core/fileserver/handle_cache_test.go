package fileserver

import (
	"bytes"
	"discovery/app/core/testutil"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSetProgressCallbackAndAllowOtherVolumesConcurrencyRace(t *testing.T) {
	tmp := testutil.TempDir(t)
	srv := NewServer(tmp)
	defer srv.Close()

	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(tmp, fmt.Sprintf("src-%d.bin", i)), bytes.Repeat([]byte{byte(i + 1)}, 8*1024), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for iter := 0; iter < 15; iter++ {
				if (id+iter)%2 == 0 {
					srv.SetProgressCallback(func(loaded, total int64) {
						_ = loaded + total
					})
					srv.SetAllowOtherVolumes(iter%2 == 0)
				} else {
					srv.SetProgressCallback(nil)
					_ = srv.AllowOtherVolumes()
				}
				resp := srv.HandleSessionRequest(FileSessionRequest{
					Version:   1,
					RequestID: fmt.Sprintf("c-%d-%d", id, iter),
					Action:    "copy",
					Path:      fmt.Sprintf("src-%d.bin", id%4),
					NewPath:   fmt.Sprintf("dst-%d-%d.bin", id, iter),
				})
				if !resp.Success {
					t.Errorf("copy failed: %s", resp.Error)
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestHandleCacheReuseAcrossChunksGetAndPut(t *testing.T) {
	tmp := testutil.TempDir(t)
	srv := newServerWithHandleCache(tmp, 8, 10*time.Second)
	defer srv.Close()

	// Chunked put (3 chunks): misses=1, hits=2, closed before rename on final chunk
	payload := bytes.Repeat([]byte("Z"), 3*1024)
	for ci := 0; ci < 3; ci++ {
		idx := ci
		resp := srv.HandleSessionRequest(FileSessionRequest{
			Version:     1,
			RequestID:   fmt.Sprintf("put-%d", ci),
			Action:      "put",
			Path:        "big.bin",
			Data:        payload[ci*1024 : (ci+1)*1024],
			ChunkIndex:  &idx,
			ChunkSize:   1024,
			TotalChunks: 3,
		})
		if !resp.Success {
			t.Fatalf("put chunk %d failed: %s", ci, resp.Error)
		}
	}
	if srv.handles.hits.Load() < 2 {
		t.Fatalf("expected >= 2 cache hits across 3 put chunks, got hits=%d misses=%d",
			srv.handles.hits.Load(), srv.handles.misses.Load())
	}
	if srv.handles.len() != 0 {
		t.Fatalf("expected 0 cached handles after final put rename, got %d", srv.handles.len())
	}

	// Chunked get (3 chunks): misses=1, hits=2
	beforeHits := srv.handles.hits.Load()
	beforeMisses := srv.handles.misses.Load()
	var got bytes.Buffer
	for ci := 0; ci < 3; ci++ {
		idx := ci
		resp := srv.HandleSessionRequest(FileSessionRequest{
			Version:    1,
			RequestID:  fmt.Sprintf("get-%d", ci),
			Action:     "get",
			Path:       "big.bin",
			ChunkIndex: &idx,
			ChunkSize:  1024,
		})
		if !resp.Success {
			t.Fatalf("get chunk %d failed: %s", ci, resp.Error)
		}
		got.Write(resp.Data)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("chunked get mismatch")
	}
	if srv.handles.misses.Load()-beforeMisses != 1 || srv.handles.hits.Load()-beforeHits != 2 {
		t.Fatalf("expected 1 miss and 2 hits for 3 get chunks, got misses=%d hits=%d",
			srv.handles.misses.Load()-beforeMisses, srv.handles.hits.Load()-beforeHits)
	}
}

func TestHandleCacheLRUEvictionAndCloseAllowsRemoveOnWindows(t *testing.T) {
	tmp := testutil.TempDir(t)
	srv := newServerWithHandleCache(tmp, 2, 10*time.Second)

	for _, name := range []string{"a.bin", "b.bin", "c.bin"} {
		if err := os.WriteFile(filepath.Join(tmp, name), bytes.Repeat([]byte("A"), 2048), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	readChunk0 := func(name string) {
		idx := 0
		resp := srv.HandleSessionRequest(FileSessionRequest{
			Version:    1,
			RequestID:  "r-" + name,
			Action:     "get",
			Path:       name,
			ChunkIndex: &idx,
			ChunkSize:  1024,
		})
		if !resp.Success {
			t.Fatalf("get %s failed: %s", name, resp.Error)
		}
	}

	readChunk0("a.bin")
	readChunk0("b.bin")
	// Touch a.bin so b.bin becomes LRU
	readChunk0("a.bin")
	// Open c.bin -> must evict and close b.bin
	readChunk0("c.bin")

	if srv.handles.len() != 2 {
		t.Fatalf("expected cache size 2 after LRU eviction, got %d", srv.handles.len())
	}
	// On Windows, removing b.bin succeeds ONLY if its handle was closed on eviction.
	if err := os.Remove(filepath.Join(tmp, "b.bin")); err != nil {
		t.Fatalf("os.Remove(b.bin) after LRU eviction failed: %v", err)
	}

	// Start an incomplete chunked put so .tmp write handle is cached, then Close().
	idx0 := 0
	putResp := srv.HandleSessionRequest(FileSessionRequest{
		Version:     1,
		RequestID:   "inc-0",
		Action:      "put",
		Path:        "incomplete.bin",
		Data:        []byte("partial"),
		ChunkIndex:  &idx0,
		ChunkSize:   1024,
		TotalChunks: 2,
	})
	if !putResp.Success {
		t.Fatalf("incomplete put failed: %s", putResp.Error)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Server.Close: %v", err)
	}
	// After Server.Close(), all cached handles (a.bin, c.bin, incomplete.bin.tmp) must be removable on Windows.
	for _, name := range []string{"a.bin", "c.bin", "incomplete.bin.tmp"} {
		if err := os.Remove(filepath.Join(tmp, name)); err != nil {
			t.Fatalf("os.Remove(%s) after Server.Close failed: %v", name, err)
		}
	}
}

func TestHandleCacheTTLExpiration(t *testing.T) {
	tmp := testutil.TempDir(t)
	srv := newServerWithHandleCache(tmp, 4, 50*time.Millisecond)
	defer srv.Close()

	target := filepath.Join(tmp, "ttl.bin")
	if err := os.WriteFile(target, bytes.Repeat([]byte("T"), 2048), 0o644); err != nil {
		t.Fatal(err)
	}

	req, _ := json.Marshal(map[string]any{
		"version":    1,
		"requestId":  "ttl-0",
		"action":     "get",
		"path":       "ttl.bin",
		"chunkIndex": 0,
		"chunkSize":  512,
	})
	if raw := srv.HandleRequest(req); !okResp(t, raw, "ttl get") {
		t.Fatal()
	}

	deadline := time.Now().Add(2 * time.Second)
	for srv.handles.len() > 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if srv.handles.len() != 0 {
		t.Fatalf("expected TTL reaper to evict handle, len=%d", srv.handles.len())
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("os.Remove after TTL expiration failed: %v", err)
	}
}

func TestHandleCacheConcurrentPutChunk0SamePathNoLeak(t *testing.T) {
	const rounds = 30
	const goroutines = 4

	for round := 0; round < rounds; round++ {
		tmp := testutil.TempDir(t)
		srv := NewServer(tmp)
		target := "race-0.bin"
		tmpPath := filepath.Join(tmp, target+".tmp")

		start := make(chan struct{})
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				<-start
				idx := 0
				resp := srv.HandleSessionRequest(FileSessionRequest{
					Version:     1,
					RequestID:   fmt.Sprintf("r-%d-%d", round, id),
					Action:      "put",
					Path:        target,
					Data:        []byte(fmt.Sprintf("chunk-%d-%d", round, id)),
					ChunkIndex:  &idx,
					ChunkSize:   64,
					TotalChunks: 2,
				})
				if !resp.Success {
					t.Errorf("round %d goroutine %d put(chunkIndex=0) failed: %s", round, id, resp.Error)
				}
			}(g)
		}
		close(start)
		wg.Wait()

		srv.handles.mu.Lock()
		entriesLen := len(srv.handles.entries)
		lruLen := srv.handles.lru.Len()
		srv.handles.mu.Unlock()
		if entriesLen != lruLen || entriesLen > 1 {
			t.Fatalf("round %d handle cache invariant broken: entries=%d lru=%d", round, entriesLen, lruLen)
		}

		if err := srv.Close(); err != nil {
			t.Fatalf("round %d srv.Close: %v", round, err)
		}
		if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
			t.Fatalf("round %d os.Remove(%s) after srv.Close() leaked handle: %v", round, tmpPath, err)
		}
	}
}
