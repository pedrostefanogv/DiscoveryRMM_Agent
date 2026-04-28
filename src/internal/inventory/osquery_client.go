package inventory

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	osquery "github.com/osquery/osquery-go"

	"discovery/internal/errutil"
	"discovery/internal/processutil"
)

const (
	// socketConnectTimeout is the maximum time to wait when trying to connect
	// to an existing osquery extension socket.
	socketConnectTimeout = 2 * time.Second

	// socketReadyTimeout is how long to wait for a freshly-started osqueryi
	// to create its extension socket.
	socketReadyTimeout = 15 * time.Second

	// socketProbeInterval controls how often we retry the connect probe.
	socketProbeInterval = 100 * time.Millisecond

	// socketQueryRetryDelay controls backoff between retries for transient
	// socket query failures.
	socketQueryRetryDelay = 150 * time.Millisecond

	// socketQueryMaxAttempts limits retry attempts for each query.
	socketQueryMaxAttempts = 2
)

// osquerydSocketPaths returns platform-specific candidate paths where a
// running osqueryd daemon would expose its extension socket.
func osquerydSocketPaths() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			`\\.\pipe\osquery.em`,
		}
	case "darwin":
		return []string{
			"/private/var/osquery/osquery.em",
			filepath.Join(os.Getenv("HOME"), ".osquery", "shell.em"),
		}
	default: // linux
		return []string{
			"/var/osquery/osquery.em",
		}
	}
}

// findOsquerydSocket returns the socket path of a running osqueryd daemon,
// or an empty string if no daemon socket is detected.
func findOsquerydSocket() string {
	for _, path := range osquerydSocketPaths() {
		// On Windows, named pipes don't respond to os.Stat; try a real connect.
		if runtime.GOOS == "windows" {
			if c, err := osquery.NewClient(path, 500*time.Millisecond); err == nil {
				c.Close()
				return path
			}
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// buildSocketPath returns a process-unique socket path for a transient
// osqueryi instance started by this process.
func buildSocketPath() string {
	pid := strconv.Itoa(os.Getpid())
	switch runtime.GOOS {
	case "windows":
		return `\\.\pipe\discovery_osquery_` + pid
	default:
		return filepath.Join(os.TempDir(), "discovery_osquery_"+pid+".em")
	}
}

// waitForSocket polls until the osquery socket accepts connections or the
// deadline passes.
func waitForSocket(ctx context.Context, socketPath string) error {
	deadline := time.Now().Add(socketReadyTimeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c, err := osquery.NewClient(socketPath, 500*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(socketProbeInterval)
	}
	return fmt.Errorf("timeout aguardando socket osquery em %s", socketPath)
}

// osqueryiSocketProcess wraps an osqueryi process running in socket mode so
// it can be stopped cleanly.
type osqueryiSocketProcess struct {
	socketPath string
	cmd        *exec.Cmd
	mu         sync.Mutex
	stopped    bool
}

func (p *osqueryiSocketProcess) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	if p.cmd != nil && p.cmd.Process != nil {
		errutil.LogIfErr(p.cmd.Process.Kill(), "osquery: kill processo")
		_, waitErr := p.cmd.Process.Wait()
		errutil.LogIfErr(waitErr, "osquery: wait processo")
	}
	if runtime.GOOS != "windows" {
		errutil.LogIfErr(os.Remove(p.socketPath), "osquery: remover socket")
	}
}

// startOsqueryiSocket launches osqueryi with --extensions_socket and waits
// until the socket is ready.  The caller must call stop() to clean up.
func startOsqueryiSocket(ctx context.Context, binary string) (*osqueryiSocketProcess, error) {
	socketPath := buildSocketPath()

	cmd := exec.CommandContext(ctx, binary,
		"--nodisable_extensions",
		"--extensions_socket", socketPath,
	)
	processutil.HideWindow(cmd)
	// Discard stdin/stdout so the interactive REPL does not block.
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("erro ao iniciar osqueryi em modo socket: %w", err)
	}

	proc := &osqueryiSocketProcess{socketPath: socketPath, cmd: cmd}
	if err := waitForSocket(ctx, socketPath); err != nil {
		proc.stop()
		return nil, err
	}
	return proc, nil
}

// runQueriesViaSocket executes all queries through a single osquery-go
// Thrift client connected to socketPath.  Results are returned keyed by
// query name in the same format as runParallelQueries.
//
// Using a single persistent connection avoids the per-query subprocess-spawn
// overhead that the subprocess approach incurs, resulting in lower latency
// and resource usage when many queries are executed together.
func runQueriesViaSocket(ctx context.Context, socketPath string, queries []osqueryQuery, progress func()) map[string]osqueryResult {
	client, err := osquery.NewClient(socketPath, socketConnectTimeout)
	if err != nil {
		// Return all results as errors so the caller can fall back.
		m := make(map[string]osqueryResult, len(queries))
		for _, q := range queries {
			m[q.name] = osqueryResult{name: q.name, err: fmt.Errorf("erro ao conectar ao socket osquery: %w", err)}
		}
		return m
	}
	defer client.Close()

	if progress != nil {
		progress()
	}

	m := make(map[string]osqueryResult, len(queries))
	for _, q := range queries {
		if ctx.Err() != nil {
			m[q.name] = osqueryResult{name: q.name, err: ctx.Err()}
			continue
		}

		rowsRaw, statusCode, statusMessage, qErr := queryWithRetry(ctx, client, q.sql)
		if qErr != nil {
			m[q.name] = osqueryResult{name: q.name, err: fmt.Errorf("erro ao executar query %q: %w", q.name, qErr)}
		} else if statusCode != 0 {
			m[q.name] = osqueryResult{name: q.name, err: fmt.Errorf("osquery erro em %q: %s", q.name, statusMessage)}
		} else {
			// Convert []map[string]string → []map[string]any to match the
			// type expected by the rest of the inventory pipeline.
			rows := make([]map[string]any, len(rowsRaw))
			for i, row := range rowsRaw {
				r := make(map[string]any, len(row))
				for k, v := range row {
					r[k] = v
				}
				rows[i] = r
			}
			m[q.name] = osqueryResult{name: q.name, rows: rows}
		}

		if progress != nil {
			progress()
		}
	}
	return m
}

func queryWithRetry(ctx context.Context, client *osquery.ExtensionManagerClient, sql string) ([]map[string]string, int, string, error) {
	var lastErr error
	for attempt := 1; attempt <= socketQueryMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, 0, "", ctx.Err()
		}

		resp, err := client.Query(sql)
		if err == nil {
			statusCode := 0
			statusMessage := ""
			if resp.Status != nil {
				statusCode = int(resp.Status.Code)
				statusMessage = resp.Status.Message
			}
			return resp.Response, statusCode, statusMessage, nil
		}

		lastErr = err
		if attempt == socketQueryMaxAttempts {
			break
		}

		timer := time.NewTimer(socketQueryRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, 0, "", ctx.Err()
		case <-timer.C:
		}
	}

	return nil, 0, "", lastErr
}
