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
	"strings"
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

// A API serializa FileScope como string (JsonStringEnumConverter global em
// Program.cs). Um decode direto em int abortava a listagem inteira da aba
// Arquivos; este teste fixa o payload REAL do servidor.
func TestAgentFileItem_DecodesApiStringScopeAndFolderPath(t *testing.T) {
	raw := []byte(`[
		{"id":"g1","name":"Global Tool","fileName":"tool.exe","contentType":"application/octet-stream","sizeBytes":10,"scope":"Global","folderPath":"/Utilitarios"},
		{"id":"c1","name":"Client Tool","fileName":"c.exe","contentType":"application/octet-stream","sizeBytes":20,"scope":"Client","folderPath":"/"},
		{"id":"s1","name":"Site Tool","fileName":"s.exe","contentType":"application/octet-stream","sizeBytes":30,"scope":"Site"}
	]`)

	var items []AgentFileItem
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("unmarshal com scope string: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("esperava 3 itens, veio %d", len(items))
	}
	want := []int{0, 1, 2}
	for i, w := range want {
		if items[i].Scope != w {
			t.Fatalf("item %d: scope = %d, esperado %d", i, items[i].Scope, w)
		}
	}
	if items[0].FolderPath != "/Utilitarios" {
		t.Fatalf("folderPath perdido: %q", items[0].FolderPath)
	}

	// O frontend compara String(f.scope) com "0"/"1"/"2": o reemitir precisa
	// continuar numérico mesmo tendo aceitado string na entrada.
	encoded, err := json.Marshal(items[2])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"scope":2`) {
		t.Fatalf("scope reemitido nao numerico: %s", encoded)
	}

	// Escopo desconhecido (enum novo na API) não pode derrubar a listagem inteira:
	// o item é mantido com 0 (Global), como o frontend já faz no rótulo.
	var unknown []AgentFileItem
	if err := json.Unmarshal([]byte(`[{"id":"u1","scope":"Department"},{"id":"u2","scope":"Site"}]`), &unknown); err != nil {
		t.Fatalf("escopo desconhecido nao pode falhar o decode: %v", err)
	}
	if len(unknown) != 2 || unknown[0].Scope != 0 || unknown[1].Scope != 2 {
		t.Fatalf("escopo desconhecido tratado errado: %+v", unknown)
	}

	// Compatibilidade: payload numérico (portal/versões antigas) segue valendo.
	var numeric []AgentFileItem
	if err := json.Unmarshal([]byte(`[{"id":"n1","scope":1}]`), &numeric); err != nil {
		t.Fatalf("unmarshal com scope numerico: %v", err)
	}
	if numeric[0].Scope != 1 {
		t.Fatalf("scope numerico = %d, esperado 1", numeric[0].Scope)
	}
}

func TestHandleIPCRequest_FilesListAndDownloadURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mdz_test-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent-auth/me/files":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"file-1","name":"Installer","fileName":"setup.exe","contentType":"application/octet-stream","sizeBytes":1234,"scope":0}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent-auth/me/files/file-1/download-url":
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
