package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	debugsvc "discovery/app/debug"
)

func TestStreamDownloadAndVerify_Sha256MatchAndMismatch(t *testing.T) {
	payload := []byte("discovery-agent-file-payload-123456")
	sum := sha256.Sum256(payload)
	validSha := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	a := &App{}
	dir := t.TempDir()

	// 1. SHA-256 válido
	okTarget := filepath.Join(dir, "ok.bin")
	res, err := a.streamDownloadAndVerify(context.Background(), "f1", srv.URL, okTarget, "ok.bin", int64(len(payload)), validSha)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !res.Verified || res.SizeBytes != int64(len(payload)) {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, err := os.Stat(okTarget); err != nil {
		t.Fatalf("expected file at %s: %v", okTarget, err)
	}

	// 2. SHA-256 inválido -> erro e arquivo temporário removido
	badTarget := filepath.Join(dir, "bad.bin")
	_, err = a.streamDownloadAndVerify(context.Background(), "f2", srv.URL, badTarget, "bad.bin", int64(len(payload)), "0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if _, statErr := os.Stat(badTarget); !os.IsNotExist(statErr) {
		t.Fatalf("expected corrupted file not to exist at %s", badTarget)
	}
	if matches, globErr := filepath.Glob(filepath.Join(dir, "*.part")); globErr != nil || len(matches) != 0 {
		t.Fatalf("expected temp .part files to be cleaned up, found=%v err=%v", matches, globErr)
	}
}

func TestAgentFileItem_JsonRoundTrip(t *testing.T) {
	item := AgentFileItem{
		ID:        "11111111-1111-1111-1111-111111111111",
		Name:      "Driver Pack",
		FileName:  "drivers.zip",
		SizeBytes: 2048,
		Scope:     1,
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded AgentFileItem
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded.Name != item.Name || decoded.Scope != 1 {
		t.Fatalf("unexpected decoded: %+v", decoded)
	}
}

func TestHandleIPCRequest_FilesListAndDownloadURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mdz_test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent-files":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"file-1","name":"Installer","fileName":"setup.exe","contentType":"application/octet-stream","sizeBytes":1234,"scope":0}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-files/file-1/download-url":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"downloadUrl":"https://s3.local/setup.exe","fileName":"setup.exe","sizeBytes":1234,"sha256":"abcd","expiresInSeconds":900}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := &App{}
	a.DebugSvc = debugsvc.NewService(debugsvc.Options{})
	a.DebugSvc.ApplyRuntimeConnectionConfig("", srv.URL, "mdz_test-token", "11111111-1111-1111-1111-111111111111", "", "")

	listResp := a.handleIPCRequest(context.Background(), map[string]any{
		"method": "files:list",
	})
	if ok, _ := listResp["ok"].(bool); !ok {
		t.Fatalf("files:list failed: %+v", listResp)
	}
	rawList, err := decodeIPCSupportResult("files:list", listResp)
	if err != nil {
		t.Fatalf("decode files:list: %v", err)
	}
	var items []AgentFileItem
	if err := json.Unmarshal(rawList, &items); err != nil {
		t.Fatalf("unmarshal files:list: %v", err)
	}
	if len(items) != 1 || items[0].ID != "file-1" {
		t.Fatalf("unexpected items: %+v", items)
	}

	dlResp := a.handleIPCRequest(context.Background(), map[string]any{
		"method": "files:download_url",
		"fileId": "file-1",
	})
	if ok, _ := dlResp["ok"].(bool); !ok {
		t.Fatalf("files:download_url failed: %+v", dlResp)
	}
	rawMeta, err := decodeIPCSupportResult("files:download_url", dlResp)
	if err != nil {
		t.Fatalf("decode files:download_url: %v", err)
	}
	var meta agentPresignedDownloadResp
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		t.Fatalf("unmarshal files:download_url: %v", err)
	}
	if meta.DownloadUrl != "https://s3.local/setup.exe" || meta.FileName != "setup.exe" {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	badResp := a.handleIPCRequest(context.Background(), map[string]any{
		"method": "files:download_url",
		"fileId": "",
	})
	if ok, _ := badResp["ok"].(bool); ok {
		t.Fatalf("expected files:download_url with empty fileId to fail, got: %+v", badResp)
	}
}
