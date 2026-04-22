package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"discovery/internal/models"
	"discovery/internal/processutil"
)

// osqueryQuery describes a single osquery SQL query to execute.
type osqueryQuery struct {
	name     string // human-readable label (for error messages)
	sql      string
	required bool // if true, an error is propagated to the caller
}

// osqueryResult holds the output of a single osquery query.
type osqueryResult struct {
	name string
	rows []map[string]any
	err  error
}

// queryOsquery executes a single osquery query using the given binary.
// The provided context controls the subprocess lifetime; the caller is
// responsible for setting an appropriate deadline.
func queryOsquery(ctx context.Context, binary, query string) ([]map[string]any, error) {
	cmd := exec.CommandContext(ctx, binary, "--json", query)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("erro no osqueryi: %w | saida: %s", err, strings.TrimSpace(string(output)))
	}

	var rows []map[string]any
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, fmt.Errorf("erro ao parsear json do osqueryi: %w", err)
	}
	return rows, nil
}

// maxConcurrentQueries limits the number of goroutines spawned during
// parallel osquery execution to prevent resource exhaustion.
const maxConcurrentQueries = 6

// runParallelQueries executes all queries concurrently (up to
// maxConcurrentQueries at a time) and returns results keyed by query name.
// The provided context should already have a timeout.
func runParallelQueries(ctx context.Context, binary string, queries []osqueryQuery, progress func()) map[string]osqueryResult {
	results := make([]osqueryResult, len(queries))
	var wg sync.WaitGroup
	wg.Add(len(queries))

	if progress != nil {
		progress()
	}

	sem := make(chan struct{}, maxConcurrentQueries)

	for i, q := range queries {
		go func(idx int, query osqueryQuery) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release
			rows, err := queryOsquery(ctx, binary, query.sql)
			results[idx] = osqueryResult{name: query.name, rows: rows, err: err}
			if progress != nil {
				progress()
			}
		}(i, q)
	}

	wg.Wait()

	m := make(map[string]osqueryResult, len(results))
	for _, r := range results {
		m[r.name] = r
	}
	return m
}

const (
	osqueryPositiveCacheTTL = 10 * time.Minute
	osqueryNegativeCacheTTL = 15 * time.Second
)

type osqueryBinaryCache struct {
	mu        sync.RWMutex
	path      string
	err       error
	checkedAt time.Time
}

var osqueryCache osqueryBinaryCache

// FindOsqueryBinary attempts to locate the osqueryi executable.
// Results are cached with TTL and can be invalidated after install.
func FindOsqueryBinary() (string, error) {
	now := time.Now()

	osqueryCache.mu.RLock()
	path := osqueryCache.path
	err := osqueryCache.err
	checkedAt := osqueryCache.checkedAt
	osqueryCache.mu.RUnlock()

	if !checkedAt.IsZero() {
		age := now.Sub(checkedAt)
		if err == nil && path != "" {
			if age < osqueryPositiveCacheTTL {
				if _, statErr := os.Stat(path); statErr == nil {
					return path, nil
				}
			}
		} else if age < osqueryNegativeCacheTTL {
			return "", err
		}
	}

	resolvedPath, resolveErr := resolveOsqueryBinary()
	osqueryCache.mu.Lock()
	osqueryCache.path = resolvedPath
	osqueryCache.err = resolveErr
	osqueryCache.checkedAt = now
	osqueryCache.mu.Unlock()

	return resolvedPath, resolveErr
}

// InvalidateOsqueryBinaryCache forces the next FindOsqueryBinary call to re-check PATH/filesystem.
func InvalidateOsqueryBinaryCache() {
	osqueryCache.mu.Lock()
	osqueryCache.path = ""
	osqueryCache.err = nil
	osqueryCache.checkedAt = time.Time{}
	osqueryCache.mu.Unlock()
}

func resolveOsqueryBinary() (string, error) {
	candidates := []string{
		"osqueryi.exe",
		"osqueryi",
		`C:\\Program Files\\osquery\\osqueryi.exe`,
	}

	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("osqueryi nao encontrado")
}

// GetOsqueryStatus checks whether osqueryi is available on this machine.
func GetOsqueryStatus() models.OsqueryStatus {
	path, err := FindOsqueryBinary()
	if err != nil {
		return models.OsqueryStatus{
			Installed:          false,
			Path:               "",
			SuggestedPackageID: "osquery.osquery",
		}
	}

	return models.OsqueryStatus{
		Installed:          true,
		Path:               path,
		SuggestedPackageID: "osquery.osquery",
	}
}
