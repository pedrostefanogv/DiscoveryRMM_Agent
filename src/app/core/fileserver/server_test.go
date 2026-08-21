package fileserver

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHandleRequest_ListPutGetRenameDelete valida o ciclo completo do protocolo v1:
// list → put (chunked) → get (chunked) → rename → delete + path traversal.
func TestHandleRequest_ListPutGetRenameDelete(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	// ── list raiz (vazia) ──
	resp := srv.HandleRequest([]byte(`{"version":1,"requestId":"r1","action":"list","path":""}`))
	var listResp FileSessionResponse
	if err := json.Unmarshal(resp, &listResp); err != nil {
		t.Fatalf("list: unmarshal: %v", err)
	}
	if !listResp.Success {
		t.Fatalf("list: success=false, err=%s", listResp.Error)
	}

	// ── put chunked (2 chunks de 16 bytes de um total de 32) ──
	content := []byte("0123456789abcdefghijklmnopqrstuv") // 32 bytes
	chunk0 := content[0:16]
	chunk1 := content[16:32]
	putReq0 := mkPutReq("r2", "arquivo.bin", chunk0, 0, 16, 2)
	if r := srv.HandleRequest(putReq0); !okResp(t, r, "put chunk0") {
		t.Fatal()
	}
	putReq1 := mkPutReq("r3", "arquivo.bin", chunk1, 1, 16, 2)
	if r := srv.HandleRequest(putReq1); !okResp(t, r, "put chunk1") {
		t.Fatal()
	}

	// ── get chunked (chunk 0 e 1) ──
	getReq0 := []byte(`{"version":1,"requestId":"r4","action":"get","path":"arquivo.bin","chunkIndex":0,"chunkSize":16}`)
	r0 := srv.HandleRequest(getReq0)
	var get0 FileSessionResponse
	_ = json.Unmarshal(r0, &get0)
	if !get0.Success || get0.TotalChunks != 2 {
		t.Fatalf("get chunk0: success=%v totalChunks=%d err=%s", get0.Success, get0.TotalChunks, get0.Error)
	}
	if string(get0.Data) != string(chunk0) {
		t.Fatalf("get chunk0: data mismatch: got %q want %q", get0.Data, chunk0)
	}

	getReq1 := []byte(`{"version":1,"requestId":"r5","action":"get","path":"arquivo.bin","chunkIndex":1,"chunkSize":16}`)
	r1 := srv.HandleRequest(getReq1)
	var get1 FileSessionResponse
	_ = json.Unmarshal(r1, &get1)
	if !get1.Success || string(get1.Data) != string(chunk1) {
		t.Fatalf("get chunk1: success=%v data=%q err=%s", get1.Success, get1.Data, get1.Error)
	}

	// ── list deve mostrar 1 arquivo de 32 bytes ──
	lf := srv.HandleRequest([]byte(`{"version":1,"requestId":"r6","action":"list","path":""}`))
	var lr FileSessionResponse
	_ = json.Unmarshal(lf, &lr)
	if len(lr.Entries) != 1 || lr.Entries[0].Size != 32 {
		t.Fatalf("list pos-put: entries=%+v", lr.Entries)
	}

	// ── rename ──
	rn := srv.HandleRequest([]byte(`{"version":1,"requestId":"r7","action":"rename","path":"arquivo.bin","newPath":"renomeado.bin"}`))
	if !okResp(t, rn, "rename") {
		t.Fatal()
	}
	if _, err := os.Stat(filepath.Join(tmp, "renomeado.bin")); err != nil {
		t.Fatalf("rename: arquivo nao existe: %v", err)
	}

	// ── delete ──
	dl := srv.HandleRequest([]byte(`{"version":1,"requestId":"r8","action":"delete","path":"renomeado.bin"}`))
	if !okResp(t, dl, "delete") {
		t.Fatal()
	}
	if _, err := os.Stat(filepath.Join(tmp, "renomeado.bin")); !os.IsNotExist(err) {
		t.Fatalf("delete: arquivo ainda existe: %v", err)
	}
}

// TestHandleRequest_PathTraversal valida que `..\..` e absolutos fora do base são bloqueados.
func TestHandleRequest_PathTraversal(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(filepath.Join(tmp, "sandbox"))
	if err := os.MkdirAll(filepath.Join(tmp, "sandbox"), 0755); err != nil {
		t.Fatal(err)
	}
	// arquivo fora do sandbox
	outside := filepath.Join(tmp, "fora.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}

	// list com traversal
	r := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "t1", "action": "list", "path": `..\..`}))
	var resp FileSessionResponse
	_ = json.Unmarshal(r, &resp)
	if resp.Success {
		t.Fatal("traversal list: deveria falhar")
	}

	// get absoluto fora do sandbox
	r2 := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "t2", "action": "get", "path": outside}))
	var resp2 FileSessionResponse
	_ = json.Unmarshal(r2, &resp2)
	if resp2.Success {
		t.Fatal("traversal get: deveria falhar")
	}

	// delete absoluto fora do sandbox
	r3 := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "t3", "action": "delete", "path": outside}))
	var resp3 FileSessionResponse
	_ = json.Unmarshal(r3, &resp3)
	if resp3.Success {
		t.Fatal("traversal delete: deveria falhar")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("traversal delete: arquivo fora foi apagado: %v", err)
	}
}

// TestHandleRequest_ZipUnzip valida compactar (zip) e descompactar (unzip).
func TestHandleRequest_ZipUnzip(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	// Cria arquivo e pasta para compactar.
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("conteudo-a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "pasta"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "pasta", "b.txt"), []byte("conteudo-b"), 0644); err != nil {
		t.Fatal(err)
	}

	// ── zip da pasta ──
	z := srv.HandleRequest(mustJSON(t, map[string]any{
		"version": 1, "requestId": "z1", "action": "zip",
		"path": "pasta", "newPath": "pasta.zip",
	}))
	if !okResp(t, z, "zip") {
		t.Fatal()
	}
	if _, err := os.Stat(filepath.Join(tmp, "pasta.zip")); err != nil {
		t.Fatalf("zip: pasta.zip nao existe: %v", err)
	}

	// ── unzip para destino ──
	u := srv.HandleRequest(mustJSON(t, map[string]any{
		"version": 1, "requestId": "z2", "action": "unzip",
		"path": "pasta.zip", "newPath": "extraido",
	}))
	if !okResp(t, u, "unzip") {
		t.Fatal()
	}
	// Verifica conteúdo extraído.
	data, err := os.ReadFile(filepath.Join(tmp, "extraido", "b.txt"))
	if err != nil {
		t.Fatalf("unzip: b.txt nao extraido: %v", err)
	}
	if string(data) != "conteudo-b" {
		t.Fatalf("unzip: conteudo b.txt = %q", data)
	}

	// ── zip de arquivo individual ──
	zf := srv.HandleRequest(mustJSON(t, map[string]any{
		"version": 1, "requestId": "z3", "action": "zip",
		"path": "a.txt", "newPath": "a.zip",
	}))
	if !okResp(t, zf, "zip arquivo") {
		t.Fatal()
	}
	if _, err := os.Stat(filepath.Join(tmp, "a.zip")); err != nil {
		t.Fatalf("zip arquivo: a.zip nao existe: %v", err)
	}
}

// TestHandleRequest_LegacyFallback garante compatibilidade com o protocolo legado.
func TestHandleRequest_LegacyFallback(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	r := srv.HandleRequest([]byte(`{"action":"list","path":""}`))
	var legacy struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(r, &legacy); err != nil {
		t.Fatalf("legacy list: unmarshal: %v", err)
	}
	if !legacy.Success {
		t.Fatalf("legacy list: success=false err=%s", legacy.Error)
	}
}

func mkPutReq(requestID, path string, data []byte, chunkIndex, chunkSize, totalChunks int) []byte {
	b, _ := json.Marshal(map[string]any{
		"version":     1,
		"requestId":   requestID,
		"action":      "put",
		"path":        path,
		"data":        base64.StdEncoding.EncodeToString(data), // []byte JSON = base64
		"chunkIndex":  chunkIndex,
		"chunkSize":   chunkSize,
		"totalChunks": totalChunks,
	})
	return b
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func okResp(t *testing.T, raw []byte, label string) bool {
	t.Helper()
	var resp FileSessionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("%s: unmarshal: %v (raw=%s)", label, err, raw)
	}
	if !resp.Success {
		t.Fatalf("%s: success=false err=%s", label, resp.Error)
	}
	return true
}
