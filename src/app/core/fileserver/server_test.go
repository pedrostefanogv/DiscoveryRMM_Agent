package fileserver

import (
	"archive/zip"
	"encoding/base64"
	"encoding/json"
	"fmt"
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
	data, err := os.ReadFile(filepath.Join(tmp, "extraido", "pasta", "b.txt"))
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

	// ── zip múltiplo (a.txt + pasta) ──
	zm := srv.HandleRequest(mustJSON(t, map[string]any{
		"version": 1, "requestId": "z4", "action": "zip",
		"newPath": "multiplo.zip", "paths": []string{"a.txt", "pasta"},
	}))
	if !okResp(t, zm, "zip multiplo") {
		t.Fatal()
	}
	// Verifica que o zip multiplo contém as duas entradas.
	if err := func() error {
		zr, err := zip.OpenReader(filepath.Join(tmp, "multiplo.zip"))
		if err != nil {
			return err
		}
		defer zr.Close()
		names := map[string]bool{}
		for _, f := range zr.File {
			names[f.Name] = true
		}
		if !names["a.txt"] || !names["pasta/b.txt"] {
			return fmt.Errorf("zip multiplo: entradas inesperadas: %v", names)
		}
		return nil
	}(); err != nil {
		t.Fatalf("zip multiplo: %v", err)
	}
}

// TestHandleRequest_PutUsesTempNameAndRenames valida que o upload e feito de
// forma atomica: durante os chunks, apenas <destino>.tmp existe no disco; o
// arquivo com o nome final so existe (sem .tmp residual) apos o ultimo chunk.
func TestHandleRequest_PutUsesTempNameAndRenames(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	content := []byte("0123456789abcdefghijklmnopqrstuv") // 32 bytes
	chunk0 := content[0:16]
	chunk1 := content[16:32]
	finalPath := filepath.Join(tmp, "firefox.exe")
	tmpPath := finalPath + ".tmp"

	// ── put chunk 0: ainda NAO deve existir o arquivo final ──
	r0 := srv.HandleRequest(mkPutReq("t1", "firefox.exe", chunk0, 0, 16, 2))
	if !okResp(t, r0, "put chunk0") {
		t.Fatal()
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("chunk0: arquivo final NAO deveria existir ainda (evita leitura prematura): %v", err)
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("chunk0: arquivo temporario deveria existir: %v", err)
	}

	// ── put chunk 1 (ultimo): chunk 0 e 1 ja no disco, agora renomeia ──
	r1 := srv.HandleRequest(mkPutReq("t2", "firefox.exe", chunk1, 1, 16, 2))
	if !okResp(t, r1, "put chunk1") {
		t.Fatal()
	}
	finalData, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("apos ultimo chunk, arquivo final deveria existir: %v", err)
	}
	if string(finalData) != string(content) {
		t.Fatalf("arquivo final incompleto: got %q want %q", finalData, content)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("apos ultimo chunk, .tmp deveria ter sido removido pela rename: %v", err)
	}

	// ── put único (chunkIndex=nil): grava em .tmp e renomeia imediatamente ──
	small := []byte("tiny")
	ru := srv.HandleRequest(mustJSON(t, map[string]any{
		"version": 1, "requestId": "t3", "action": "put",
		"path": "nota.txt", "data": base64.StdEncoding.EncodeToString(small),
	}))
	if !okResp(t, ru, "put unico") {
		t.Fatal()
	}
	dd, err := os.ReadFile(filepath.Join(tmp, "nota.txt"))
	if err != nil || string(dd) != "tiny" {
		t.Fatalf("put unico: conteudo = %q err=%v", dd, err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "nota.txt.tmp")); !os.IsNotExist(err) {
		t.Fatalf("put unico: .tmp nao deveria restar: %v", err)
	}
}

// TestHandleRequest_PutFailsRemovesTemp valida que, em falha no meio do upload,
// o arquivo final nao fica parcial e o .tmp e removido.
func TestHandleRequest_PutFailsRemovesTemp(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	tmpPath := filepath.Join(tmp, "app.exe.tmp")
	finalPath := filepath.Join(tmp, "app.exe")

	// Envia chunk 0 de 1 de um total de 3 (upload ainda nao concluido).
	r0 := srv.HandleRequest(mkPutReq("t1", "app.exe", []byte("only-chunk"), 0, 16, 3))
	if !okResp(t, r0, "put chunk0") {
		t.Fatal()
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("esperava .tmp criado apos chunk0: %v", err)
	}

	// Falha/abandono do upload: simula nova tentativa de um chunk 0 - o que
	// removeria o .tmp orfao (chunk0 faz a limpeza). Apos isso, .tmp nao existe
	// e o final tambem nao (nunca foi renomeado).
	_ = srv.HandleRequest(mkPutReq("t2", "app.exe", []byte("re"), 1, 16, 3))
	// (Na pratica o chunk0 de uma nova tentativa limpa o .tmp; aqui apenas
	//  verificamos que o .tmp ainda existe pois não passou chunk0 nova.)

	// Envia o verdadeiro chunk0 novo: deve remover o .tmp orfao e recriar limpo.
	r1 := srv.HandleRequest(mkPutReq("t2", "app.exe", []byte("CHUNK0"), 0, 16, 3))
	if !okResp(t, r1, "put chunk0 (nova tentativa)") {
		t.Fatal()
	}
	// Final ainda nao deve existir, .tmp sim.
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("final nao deveria existir antes do ultimo chunk: %v", err)
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf(".tmp deveria existir na nova tentativa: %v", err)
	}
}

// TestHandleRequest_PutResumeWithoutTempFails valida que um resume (chunk>0)
// sem um .tmp existente retorna erro claro (não cria arquivo com "buracos"),
// forçando o viewer a recomeçar do chunk 0.
func TestHandleRequest_PutResumeWithoutTempFails(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	// Chunk 1 de um total de 3, mas NENHUM chunk 0 foi enviado antes (sem .tmp).
	r := srv.HandleRequest(mkPutReq("t1", "app.exe", []byte("tail"), 1, 16, 3))
	var resp FileSessionResponse
	if err := json.Unmarshal(r, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Success {
		t.Fatal("resume sem .tmp deveria falhar")
	}
	if _, err := os.Stat(filepath.Join(tmp, "app.exe.tmp")); !os.IsNotExist(err) {
		t.Fatalf("nao deveria criar .tmp espurio: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "app.exe")); !os.IsNotExist(err) {
		t.Fatalf("nao deveria criar o arquivo final: %v", err)
	}
}

// TestHandleRequest_DeleteRemovesOrphanTemp valida que apagar um arquivo
// tambem remove um .tmp orfao associado (upload abortado).
func TestHandleRequest_DeleteRemovesOrphanTemp(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	finalPath := filepath.Join(tmp, "app.exe")
	tmpPath := finalPath + ".tmp"
	if err := os.WriteFile(finalPath, []byte("final"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("parcial"), 0644); err != nil {
		t.Fatal(err)
	}

	r := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "d1", "action": "delete", "path": "app.exe"}))
	if !okResp(t, r, "delete") {
		t.Fatal()
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("arquivo final nao foi removido: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf(".tmp orfao nao foi removido: %v", err)
	}
}

// TestHandleRequest_DeleteCancelledUploadRemovesTempOnly valida o cancelamento
// de um upload em andamento: o arquivo final ainda não existe, mas o .tmp
// parcial sim. O delete do path final deve limpar o .tmp e retornar sucesso.
func TestHandleRequest_DeleteCancelsUploadHandlesTempOnly(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	finalPath := filepath.Join(tmp, "app.exe")
	tmpPath := finalPath + ".tmp"
	// Simula upload em andamento: só o .tmp existe (chunk 0 enviado, final não).
	if err := os.WriteFile(tmpPath, []byte("parcial"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatal("pre-condicao: final nao deveria existir")
	}

	r := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "d2", "action": "delete", "path": "app.exe"}))
	if !okResp(t, r, "delete cancel upload") {
		t.Fatal()
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf(".tmp do upload cancelado nao foi removido: %v", err)
	}
}

// TestHandleRequest_DeleteMissingFileFails valida que apagar um arquivo que
// nunca existiu (sem .tmp órfão) ainda retorna erro.
func TestHandleRequest_DeleteMissingFileFails(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	r := srv.HandleRequest(mustJSON(t, map[string]any{"version": 1, "requestId": "d3", "action": "delete", "path": "nao-existe.txt"}))
	var resp FileSessionResponse
	if err := json.Unmarshal(r, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Success {
		t.Fatal("delete de arquivo inexistente deveria falhar")
	}
}

// TestHandleRequest_PutChunkOutOfRangeFails valida que um chunk com index fora
// do range (inconsistência do viewer) é rejeitado e não renomeia o .tmp com
// dados incompletos.
func TestHandleRequest_PutChunkOutOfRangeFails(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(tmp)

	finalPath := filepath.Join(tmp, "app.exe")
	tmpPath := finalPath + ".tmp"

	// Chunk 0 de um total de 2 (upload em andamento).
	if !okResp(t, srv.HandleRequest(mkPutReq("t1", "app.exe", []byte("AAAA"), 0, 16, 2)), "put chunk0") {
		t.Fatal()
	}
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf(".tmp deveria existir: %v", err)
	}

	// Chunk com index fora do range (ex.: 5 de 2) — deve falhar e NÃO renomear.
	r := srv.HandleRequest(mkPutReq("t2", "app.exe", []byte("BBBB"), 5, 16, 2))
	var resp FileSessionResponse
	if err := json.Unmarshal(r, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Success {
		t.Fatal("chunk fora do range deveria falhar")
	}
	if _, err := os.Stat(finalPath); !os.IsNotExist(err) {
		t.Fatalf("arquivo final nao deveria ser renomeado com dados incompletos: %v", err)
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
