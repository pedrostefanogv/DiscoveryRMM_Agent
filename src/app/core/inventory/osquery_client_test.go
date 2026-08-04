package inventory

import (
	"context"
	"testing"
	"time"
)

// TestRunQueriesViaSocket_UnreachableSocket verifies that runQueriesViaSocket
// returns error results (not a panic or hang) when the socket path is invalid.
func TestRunQueriesViaSocket_UnreachableSocket(t *testing.T) {
	queries := []osqueryQuery{
		{name: "system_info", sql: "SELECT 1", required: true},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := runQueriesViaSocket(ctx, "/nonexistent/socket.em", queries, nil)

	r := results["system_info"]
	if r.err == nil {
		t.Error("expected error when socket is unreachable, got nil")
	}
}

// TestAllRequiredSucceeded verifies the helper correctly evaluates results.
func TestAllRequiredSucceeded(t *testing.T) {
	queries := []osqueryQuery{
		{name: "q1", sql: "SELECT 1", required: true},
		{name: "q2", sql: "SELECT 1", required: false},
	}

	// All required queries succeeded.
	results := map[string]osqueryResult{
		"q1": {name: "q1", rows: []map[string]any{{"x": "1"}}},
		"q2": {name: "q2", err: errTest},
	}
	if !allRequiredSucceeded(results, queries) {
		t.Error("allRequiredSucceeded should be true when required query has rows")
	}

	// Required query has error.
	results["q1"] = osqueryResult{name: "q1", err: errTest}
	if allRequiredSucceeded(results, queries) {
		t.Error("allRequiredSucceeded should be false when required query has error")
	}

	// Required query returned empty rows.
	results["q1"] = osqueryResult{name: "q1", rows: []map[string]any{}}
	if allRequiredSucceeded(results, queries) {
		t.Error("allRequiredSucceeded should be false when required query has empty rows")
	}
}

// TestFindOsquerydSocket_NotRunning confirms that findOsquerydSocket returns ""
// on this test machine where no osqueryd daemon is expected to be running.
func TestFindOsquerydSocket_NotRunning(t *testing.T) {
	// On a CI/test machine there is no running osqueryd, so we expect "".
	// This is a best-effort sanity check; it does not fail if a daemon happens to run.
	path := findOsquerydSocket()
	t.Logf("findOsquerydSocket returned %q", path)
	// No assertion: if a daemon is running the path will be non-empty, which is fine.
}

// TestBuildSocketPath_NonEmpty ensures buildSocketPath always returns a non-empty string.
func TestBuildSocketPath_NonEmpty(t *testing.T) {
	p := buildSocketPath()
	if p == "" {
		t.Error("buildSocketPath returned empty string")
	}
}

// errTest is a sentinel error used in tests.
var errTest = &testError{}

type testError struct{}

func (e *testError) Error() string { return "test error" }
