package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"winget-store/internal/models"
	"winget-store/internal/processutil"
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
func runParallelQueries(ctx context.Context, binary string, queries []osqueryQuery) map[string]osqueryResult {
	results := make([]osqueryResult, len(queries))
	var wg sync.WaitGroup
	wg.Add(len(queries))

	sem := make(chan struct{}, maxConcurrentQueries)

	for i, q := range queries {
		go func(idx int, query osqueryQuery) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release
			rows, err := queryOsquery(ctx, binary, query.sql)
			results[idx] = osqueryResult{name: query.name, rows: rows, err: err}
		}(i, q)
	}

	wg.Wait()

	m := make(map[string]osqueryResult, len(results))
	for _, r := range results {
		m[r.name] = r
	}
	return m
}

// osqueryBinaryCache stores the result of FindOsqueryBinary for reuse.
var (
	osqueryBinaryOnce sync.Once
	osqueryBinaryPath string
	osqueryBinaryErr  error
)

// FindOsqueryBinary attempts to locate the osqueryi executable.
// The result is cached after the first successful or unsuccessful lookup.
func FindOsqueryBinary() (string, error) {
	osqueryBinaryOnce.Do(func() {
		candidates := []string{
			"osqueryi.exe",
			"osqueryi",
			`C:\\Program Files\\osquery\\osqueryi.exe`,
		}

		for _, c := range candidates {
			if path, err := exec.LookPath(c); err == nil {
				osqueryBinaryPath = path
				return
			}
		}

		osqueryBinaryErr = fmt.Errorf("osqueryi nao encontrado")
	})
	return osqueryBinaryPath, osqueryBinaryErr
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
