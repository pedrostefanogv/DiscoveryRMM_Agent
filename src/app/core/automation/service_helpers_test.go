package automation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildExecutionMetadata_IncludesAndTruncatesOutput(t *testing.T) {
	task := AutomationTask{
		Name:       "Diagnostico",
		ActionType: ActionRunScript,
	}

	shortRes := &ExecutionResult{
		Success: true,
		Output:  "  linha 1\nlinha 2  ",
	}
	rawShort := buildExecutionMetadata(task, TriggerTypeManual, "result", shortRes, nil)
	var decodedShort map[string]any
	if err := json.Unmarshal([]byte(rawShort), &decodedShort); err != nil {
		t.Fatalf("failed to unmarshal short metadata: %v", err)
	}
	if decodedShort["output"] != "linha 1\nlinha 2" {
		t.Fatalf("unexpected output: %v", decodedShort["output"])
	}
	if _, exists := decodedShort["outputTruncated"]; exists {
		t.Fatalf("did not expect outputTruncated for short output")
	}

	longRes := &ExecutionResult{
		Success: true,
		Output:  strings.Repeat("a", 9000) + "TAIL",
	}
	rawLong := buildExecutionMetadata(task, TriggerTypeManual, "result", longRes, nil)
	var decodedLong map[string]any
	if err := json.Unmarshal([]byte(rawLong), &decodedLong); err != nil {
		t.Fatalf("failed to unmarshal long metadata: %v", err)
	}
	outStr, _ := decodedLong["output"].(string)
	if len(outStr) > 8192 || !strings.HasSuffix(outStr, "TAIL") {
		t.Fatalf("expected truncated tail output <= 8192 bytes, got len=%d", len(outStr))
	}
	if decodedLong["outputTruncated"] != true {
		t.Fatalf("expected outputTruncated=true, got %v", decodedLong["outputTruncated"])
	}
}
