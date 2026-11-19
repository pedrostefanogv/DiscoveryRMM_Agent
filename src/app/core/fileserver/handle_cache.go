package fileserver

import (
	"container/list"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaxOpenHandles = 16
	defaultOpenHandleTTL  = 30 * time.Second
)

type cachedHandle struct {
	key       string
	normPath  string
	writable  bool
	ephemeral bool
	file      *os.File
	lastUsed  time.Time
	refs      int
	evicted   bool
	elem      *list.Element
	ioMu      sync.Mutex
}

// handleCache é um cache LRU + TTL limitado de handles abertos para reduzir
// CreateFile/CloseHandle repetidos em transferências chunked (get/put).
type handleCache struct {
	mu         sync.Mutex
	entries    map[string]*cachedHandle
	lru        *list.List
	maxEntries int
	ttl        time.Duration
	closed     bool
	sweepTimer *time.Timer

	hits   atomic.Int64
	misses atomic.Int64
}

func newHandleCache(maxEntries int, ttl time.Duration) *handleCache {
	if maxEntries <= 0 {
		maxEntries = defaultMaxOpenHandles
	}
	if ttl <= 0 {
		ttl = defaultOpenHandleTTL
	}
	return &handleCache{
		entries:    make(map[string]*cachedHandle),
		lru:        list.New(),
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

func normalizeCachePath(p string) string {
	return strings.ToLower(filepath.Clean(p))
}

func readHandleKey(fullPath string) string {
	return "r:" + normalizeCachePath(fullPath)
}

func writeHandleKey(tmpPath string) string {
	return "w:" + normalizeCachePath(tmpPath)
}

func (c *handleCache) acquireRead(fullPath string) (*cachedHandle, error) {
	key := readHandleKey(fullPath)
	norm := normalizeCachePath(fullPath)
	now := time.Now()

	c.mu.Lock()
	toClose := c.evictExpiredLocked(now)
	closed := c.closed
	if !closed {
		if h, ok := c.entries[key]; ok {
			h.refs++
			h.lastUsed = now
			c.lru.MoveToFront(h.elem)
			c.hits.Add(1)
			c.mu.Unlock()
			closeFiles(toClose)
			return h, nil
		}
	}
	c.mu.Unlock()
	closeFiles(toClose)

	f, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	c.misses.Add(1)

	if closed {
		return &cachedHandle{
			key:       key,
			normPath:  norm,
			ephemeral: true,
			file:      f,
		}, nil
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return &cachedHandle{
			key:       key,
			normPath:  norm,
			ephemeral: true,
			file:      f,
		}, nil
	}
	if existing, ok := c.entries[key]; ok {
		existing.refs++
		existing.lastUsed = time.Now()
		c.lru.MoveToFront(existing.elem)
		c.mu.Unlock()
		_ = f.Close()
		return existing, nil
	}

	h := &cachedHandle{
		key:      key,
		normPath: norm,
		file:     f,
		lastUsed: time.Now(),
		refs:     1,
	}
	h.elem = c.lru.PushFront(h)
	c.entries[key] = h
	overflow := c.evictOverflowLocked()
	c.armTimerLocked()
	c.mu.Unlock()
	closeFiles(overflow)
	return h, nil
}

func (c *handleCache) acquireWrite(tmpPath string, createTrunc bool) (*cachedHandle, error) {
	key := writeHandleKey(tmpPath)
	norm := normalizeCachePath(tmpPath)

	if createTrunc {
		// Garante que qualquer handle anterior para o mesmo .tmp seja fechado
		// antes de truncar/remover o arquivo (no Windows, arquivo aberto
		// bloqueia os.Remove).
		c.invalidatePath(tmpPath)
		_ = os.Remove(tmpPath)

		f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return nil, err
		}
		c.misses.Add(1)

		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return &cachedHandle{
				key:       key,
				normPath:  norm,
				writable:  true,
				ephemeral: true,
				file:      f,
			}, nil
		}
		var toClose []*os.File
		if existing, ok := c.entries[key]; ok {
			c.removeLocked(existing)
			if existing.refs <= 0 {
				toClose = append(toClose, existing.file)
			}
		}
		h := &cachedHandle{
			key:      key,
			normPath: norm,
			writable: true,
			file:     f,
			lastUsed: time.Now(),
			refs:     1,
		}
		h.elem = c.lru.PushFront(h)
		c.entries[key] = h
		overflow := c.evictOverflowLocked()
		c.armTimerLocked()
		c.mu.Unlock()
		closeFiles(toClose)
		closeFiles(overflow)
		return h, nil
	}

	now := time.Now()
	c.mu.Lock()
	toClose := c.evictExpiredLocked(now)
	closed := c.closed
	if !closed {
		if h, ok := c.entries[key]; ok {
			h.refs++
			h.lastUsed = now
			c.lru.MoveToFront(h.elem)
			c.hits.Add(1)
			c.mu.Unlock()
			closeFiles(toClose)
			return h, nil
		}
	}
	c.mu.Unlock()
	closeFiles(toClose)

	f, err := os.OpenFile(tmpPath, os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	c.misses.Add(1)

	if closed {
		return &cachedHandle{
			key:       key,
			normPath:  norm,
			writable:  true,
			ephemeral: true,
			file:      f,
		}, nil
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return &cachedHandle{
			key:       key,
			normPath:  norm,
			writable:  true,
			ephemeral: true,
			file:      f,
		}, nil
	}
	if existing, ok := c.entries[key]; ok {
		existing.refs++
		existing.lastUsed = time.Now()
		c.lru.MoveToFront(existing.elem)
		c.mu.Unlock()
		_ = f.Close()
		return existing, nil
	}

	h := &cachedHandle{
		key:      key,
		normPath: norm,
		writable: true,
		file:     f,
		lastUsed: time.Now(),
		refs:     1,
	}
	h.elem = c.lru.PushFront(h)
	c.entries[key] = h
	overflow := c.evictOverflowLocked()
	c.armTimerLocked()
	c.mu.Unlock()
	closeFiles(overflow)
	return h, nil
}

func (c *handleCache) release(h *cachedHandle, drop bool) error {
	if h == nil {
		return nil
	}
	if h.ephemeral {
		return h.file.Close()
	}

	var closeTarget *os.File
	c.mu.Lock()
	h.refs--
	h.lastUsed = time.Now()
	if drop || c.closed || h.evicted {
		c.removeLocked(h)
		if h.refs <= 0 {
			closeTarget = h.file
		}
		c.mu.Unlock()
		if closeTarget != nil {
			return closeTarget.Close()
		}
		return nil
	}
	overflow := c.evictOverflowLocked()
	c.armTimerLocked()
	c.mu.Unlock()
	closeFiles(overflow)
	return nil
}

func (c *handleCache) invalidatePath(path string) {
	norm := normalizeCachePath(path)
	c.mu.Lock()
	var toClose []*os.File
	for _, key := range []string{"r:" + norm, "w:" + norm} {
		if h, ok := c.entries[key]; ok {
			c.removeLocked(h)
			if h.refs <= 0 {
				toClose = append(toClose, h.file)
			}
		}
	}
	c.mu.Unlock()
	closeFiles(toClose)
}

func (c *handleCache) invalidatePrefix(dirPath string) {
	norm := normalizeCachePath(dirPath)
	prefix := strings.TrimRight(norm, string(os.PathSeparator)) + string(os.PathSeparator)
	c.mu.Lock()
	var toClose []*os.File
	for _, h := range c.entries {
		if h.normPath == norm || strings.HasPrefix(h.normPath, prefix) {
			c.removeLocked(h)
			if h.refs <= 0 {
				toClose = append(toClose, h.file)
			}
		}
	}
	c.mu.Unlock()
	closeFiles(toClose)
}

func (c *handleCache) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if c.sweepTimer != nil {
		c.sweepTimer.Stop()
		c.sweepTimer = nil
	}
	var toClose []*os.File
	for _, h := range c.entries {
		h.evicted = true
		h.elem = nil
		if h.refs <= 0 {
			toClose = append(toClose, h.file)
		}
	}
	c.entries = make(map[string]*cachedHandle)
	c.lru.Init()
	c.mu.Unlock()
	closeFiles(toClose)
}

func (c *handleCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *handleCache) removeLocked(h *cachedHandle) {
	h.evicted = true
	if h.elem != nil {
		c.lru.Remove(h.elem)
		h.elem = nil
	}
	if cur, ok := c.entries[h.key]; ok && cur == h {
		delete(c.entries, h.key)
	}
}

func (c *handleCache) evictOverflowLocked() []*os.File {
	var toClose []*os.File
	for elem := c.lru.Back(); elem != nil && len(c.entries) > c.maxEntries; {
		prev := elem.Prev()
		h := elem.Value.(*cachedHandle)
		if h.refs == 0 {
			c.removeLocked(h)
			toClose = append(toClose, h.file)
		}
		elem = prev
	}
	return toClose
}

func (c *handleCache) evictExpiredLocked(now time.Time) []*os.File {
	if c.ttl <= 0 {
		return nil
	}
	var toClose []*os.File
	for elem := c.lru.Back(); elem != nil; {
		prev := elem.Prev()
		h := elem.Value.(*cachedHandle)
		if now.Sub(h.lastUsed) < c.ttl {
			// LRU é ordenado por uso mais recente: daqui para frente nada expirou.
			break
		}
		if h.refs == 0 {
			c.removeLocked(h)
			toClose = append(toClose, h.file)
		}
		elem = prev
	}
	return toClose
}

func (c *handleCache) armTimerLocked() {
	if c.closed || c.ttl <= 0 || len(c.entries) == 0 {
		return
	}
	if c.sweepTimer == nil {
		c.sweepTimer = time.AfterFunc(c.ttl, c.sweepExpired)
	}
}

func (c *handleCache) sweepExpired() {
	now := time.Now()
	c.mu.Lock()
	if c.closed {
		c.sweepTimer = nil
		c.mu.Unlock()
		return
	}
	toClose := c.evictExpiredLocked(now)
	c.sweepTimer = nil
	if elem := c.lru.Back(); elem != nil {
		oldest := elem.Value.(*cachedHandle)
		delay := c.ttl - now.Sub(oldest.lastUsed)
		if delay < time.Millisecond {
			delay = time.Millisecond
		}
		c.sweepTimer = time.AfterFunc(delay, c.sweepExpired)
	}
	c.mu.Unlock()
	closeFiles(toClose)
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}
