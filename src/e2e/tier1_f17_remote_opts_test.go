package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"discovery/app/core/fileserver"
	"discovery/app/core/remotesession"
	"discovery/app/core/screen"
	"discovery/app/core/terminal"
)

func TestF17_T1_FilesBinaryDFTransferGetAndPut(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "55555555-5555-5555-5555-555555555551"
	)

	rootDir := t.TempDir()
	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")

	readySub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "files.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "files.resp"))

	handler := remotesession.NewNatsStreamHandler(agentNC, clientID, siteID, agentID)
	sf := remotesession.NewSessionFiles(sessionID, handler, rootDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sf.Start(ctx) }()
	t.Cleanup(func() {
		sf.Stop()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})

	_ = waitSubMsg(t, readySub, 5*time.Second)

	// Upload de 768 KiB binarios via frames 'DF' (3 chunks de 256 KiB)
	payload := make([]byte, 768*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	wantHash := sha256Hex(payload)
	reqSubj := sessionSubject(clientID, siteID, agentID, sessionID, "files.req")

	const chunkSz = 256 * 1024
	for idx := 0; idx < 3; idx++ {
		chunk := payload[idx*chunkSz : (idx+1)*chunkSz]
		frame := encodeBinaryFileFrame(map[string]any{
			"version":     1,
			"requestId":   fmt.Sprintf("put-%d", idx),
			"transferId":  "tx-df-1",
			"action":      "put",
			"path":        "upload.bin",
			"chunkIndex":  idx,
			"totalChunks": 3,
			"chunkSize":   chunkSz,
		}, chunk)
		if err := viewerNC.Publish(reqSubj, frame); err != nil {
			t.Fatalf("Publish DF chunk %d: %v", idx, err)
		}
		_ = viewerNC.Flush()

		msg := waitSubMsg(t, respSub, 5*time.Second)
		var resp map[string]any
		if err := json.Unmarshal(msg.Data, &resp); err != nil || resp["success"] != true {
			t.Fatalf("F17 violacao: put binario 'DF' chunk %d falhou (err=%v, data=%s)", idx, err, string(msg.Data))
		}
	}

	onDisk, err := os.ReadFile(filepath.Join(rootDir, "upload.bin"))
	if err != nil {
		t.Fatalf("ReadFile(upload.bin): %v", err)
	}
	if sha256Hex(onDisk) != wantHash {
		t.Fatalf("SHA-256 do arquivo gravado diverge: got=%s want=%s", sha256Hex(onDisk), wantHash)
	}

	// Compatibilidade JSON legado (list)
	listReq, _ := json.Marshal(map[string]any{"version": 1, "requestId": "list-1", "action": "list", "path": "."})
	_ = viewerNC.Publish(reqSubj, listReq)
	_ = viewerNC.Flush()
	listMsg := waitSubMsg(t, respSub, 5*time.Second)
	var listResp map[string]any
	if err := json.Unmarshal(listMsg.Data, &listResp); err != nil || listResp["success"] != true {
		t.Fatalf("list JSON legado falhou: %s", string(listMsg.Data))
	}
}

func TestF17_T1_FilesOpenHandleCacheAcrossChunks(t *testing.T) {
	rootDir := t.TempDir()
	srv := fileserver.NewServer(rootDir)

	chunkA := bytes.Repeat([]byte("A"), 64*1024)
	chunkB := bytes.Repeat([]byte("B"), 64*1024)

	for i, data := range [][]byte{chunkA, chunkB} {
		idx := i
		req, _ := json.Marshal(fileserver.FileSessionRequest{
			Version:     1,
			RequestID:   fmt.Sprintf("hc-%d", i),
			Action:      "put",
			Path:        "cached.bin",
			Data:        data,
			ChunkIndex:  &idx,
			TotalChunks: 2,
			ChunkSize:   64 * 1024,
		})
		rawResp := srv.HandleRequest(req)
		var resp fileserver.FileSessionResponse
		if err := json.Unmarshal(rawResp, &resp); err != nil || !resp.Success {
			t.Fatalf("put chunk %d falhou: %s", i, string(rawResp))
		}
	}

	target := filepath.Join(rootDir, "cached.bin")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != 128*1024 {
		t.Fatalf("tamanho inesperado: %d", len(got))
	}
	// No Windows, handle aberto impede os.Remove; garante fechamento ao final do ultimo chunk
	if err := os.Remove(target); err != nil {
		t.Fatalf("handle de arquivo vazado apos chunk final (os.Remove falhou): %v", err)
	}
}

func TestF17_T1_FilesSetProgressCallbackConcurrencyRace(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "55555555-5555-5555-5555-555555555552"
	)

	rootDir := t.TempDir()
	for i := 0; i < 4; i++ {
		_ = os.WriteFile(filepath.Join(rootDir, fmt.Sprintf("f%d.txt", i)), bytes.Repeat([]byte("x"), 16*1024), 0o600)
	}

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	readySub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "files.ready"))
	respSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "files.resp"))

	handler := remotesession.NewNatsStreamHandler(agentNC, clientID, siteID, agentID)
	sf := remotesession.NewSessionFiles(sessionID, handler, rootDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sf.Start(ctx) }()
	t.Cleanup(func() {
		sf.Stop()
		<-done
	})

	_ = waitSubMsg(t, readySub, 5*time.Second)

	reqSubj := sessionSubject(clientID, siteID, agentID, sessionID, "files.req")
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			var req []byte
			if id%2 == 0 {
				req, _ = json.Marshal(map[string]any{
					"version":   1,
					"requestId": fmt.Sprintf("conc-%d", id),
					"action":    "copy",
					"path":      fmt.Sprintf("f%d.txt", id%4),
					"newPath":   fmt.Sprintf("copy-%d.txt", id),
				})
			} else {
				req, _ = json.Marshal(map[string]any{
					"version":   1,
					"requestId": fmt.Sprintf("conc-%d", id),
					"action":    "list",
					"path":      ".",
				})
			}
			_ = viewerNC.Publish(reqSubj, req)
		}(i)
	}
	wg.Wait()
	_ = viewerNC.Flush()

	seen := map[string]bool{}
	for len(seen) < workers {
		msg := waitSubMsg(t, respSub, 5*time.Second)
		var resp map[string]any
		_ = json.Unmarshal(msg.Data, &resp)
		if rid, ok := resp["requestId"].(string); ok && rid != "" {
			seen[rid] = true
		}
	}
}

func TestF17_T1_HeadlessExecutionWithoutInteractiveWorker(t *testing.T) {
	const (
		clientID = "11111111-1111-1111-1111-111111111111"
		siteID   = "22222222-2222-2222-2222-222222222222"
		agentID  = "33333333-3333-3333-3333-333333333333"
	)

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")
	mgr := newTestSessionManager(t, agentNC, clientID, siteID, agentID)
	// Sem BannerFactory, sem TrayFactory, sem ConsentPrompt (modo headless Session 0)

	sidFiles := "55555555-5555-5555-5555-555555555561"
	sidProc := "55555555-5555-5555-5555-555555555562"
	sidProxy := "55555555-5555-5555-5555-555555555563"

	subFilesReady := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidFiles, "files.ready"))
	subProcReady := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidProc, "proc.ready"))
	subProxyReady := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sidProxy, "proxy.ready"))

	for _, cmd := range []map[string]any{
		{"action": "start", "sessionId": sidFiles, "kind": "files", "rootPath": t.TempDir()},
		{"action": "start", "sessionId": sidProc, "kind": "processes"},
		{"action": "start", "sessionId": sidProxy, "kind": "proxy"},
	} {
		if ok, errMsg := mgr.HandleCommand(context.Background(), cmd); !ok {
			t.Fatalf("start headless %v falhou: %s", cmd["kind"], errMsg)
		}
	}

	_ = waitSubMsg(t, subFilesReady, 5*time.Second)
	_ = waitSubMsg(t, subProcReady, 5*time.Second)
	_ = waitSubMsg(t, subProxyReady, 5*time.Second)

	// Verifica tambem que o roteador de comandos em app/ executa files/processes/proxy/rdp em ServiceMode sem worker.
	// NOTA (bug de spec corrigido): a checagem antiga exigia o literal 'case "files", "processes", "proxy"',
	// mas o roteador real usa as constantes remotesession.KindFiles/KindProcesses/KindProxy/KindRdp
	// dentro de isHeadlessRemoteSessionKind — o que o teste precisa garantir é a ROTA headless.
	remoteCmdSrc := readSrcFile(t, filepath.Join("app", "remote_debug_commands.go"))
	if !strings.Contains(remoteCmdSrc, "isHeadlessRemoteSessionKind(kind)") {
		t.Fatalf("F17 violacao: app/remote_debug_commands.go nao roteia sessoes headless (files/processes/proxy/rdp) sem worker interativo")
	}
}

func TestF17_T1_ScreenDXGIFirstAndGDIFallbackBufferReuse(t *testing.T) {
	// 1. Verifica prioridade DXGI-first e GDI fallback
	if c, err := screen.NewCapturerMode(0, false); err == nil && c != nil {
		name := c.Name()
		_ = c.Close()
		if name != "dxgi" && name != "gdi" {
			t.Fatalf("capturer inesperado: %s", name)
		}
	}

	// 2. Verifica pool de frames + imutabilidade + reuso de buffers no encoder JPEG
	const w, h = 1920, 1080
	frame := screen.GetPooledFrame(w, h, w*4)
	for i := 0; i < len(frame.Data); i += 4 {
		frame.Data[i] = 0x10
		frame.Data[i+1] = 0x20
		frame.Data[i+2] = 0x30
		frame.Data[i+3] = 0xFF
	}
	snapshot := make([]byte, 64)
	copy(snapshot, frame.Data[:64])

	enc := screen.NewJPEGEncoder()
	// Warmup
	out, err := enc.Encode(frame, 60)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(out) < 2 || out[0] != 0xFF || out[1] != 0xD8 {
		t.Fatalf("JPEG magic invalido: %x", out[:minInt(4, len(out))])
	}
	if !bytes.Equal(frame.Data[:64], snapshot) {
		t.Fatalf("Encoder mutou frame.Data in-place")
	}

	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)
	for i := 0; i < 4; i++ {
		if _, err := enc.Encode(frame, 60); err != nil {
			t.Fatalf("Encode(%d): %v", i, err)
		}
	}
	runtime.ReadMemStats(&m2)
	avgAllocPerFrame := (m2.TotalAlloc - m1.TotalAlloc) / 4
	// Pre-M2 alocava ~8.3 MB (w*h*4) por frame; com pool deve ser < 1.5 MB/frame
	if avgAllocPerFrame > 2*1024*1024 {
		t.Fatalf("F17 violacao: encoder JPEG aloca %d bytes/frame (> 2 MiB) sem reuso de buffer", avgAllocPerFrame)
	}
	screen.PutPooledFrame(frame)
}

func TestF17_T1_ClipboardEventDrivenAndExplicitPasteFlag(t *testing.T) {
	mgrSrc := readSrcFile(t, filepath.Join("app", "core", "remotesession", "manager.go"))
	if strings.Contains(mgrSrc, "time.NewTicker(1 * time.Second)") && strings.Contains(mgrSrc, "handleClipboardUpdate") {
		t.Fatalf("F17 violacao: manager.go ainda faz polling de clipboard a cada 1s ao inves de monitoramento event-driven")
	}
	if !strings.Contains(mgrSrc, "paste") {
		t.Fatalf("F17 violacao: .clipboard.req nao respeita flag explicita `paste` (evitando Ctrl+V incondicional)")
	}
}

func TestF17_T1_TerminalTabCompletionAndBurstCoalescing(t *testing.T) {
	const (
		clientID  = "11111111-1111-1111-1111-111111111111"
		siteID    = "22222222-2222-2222-2222-222222222222"
		agentID   = "33333333-3333-3333-3333-333333333333"
		sessionID = "55555555-5555-5555-5555-555555555577"
	)

	_, natsURL := startEmbeddedNATS(t, false, nil)
	agentNC := newTestNATSClient(t, natsURL, "")
	viewerNC := newTestNATSClient(t, natsURL, "")

	termOutSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "term.out"))
	compResSub := subscribeSync(t, viewerNC, sessionSubject(clientID, siteID, agentID, sessionID, "completion.res"))

	handler := remotesession.NewNatsStreamHandler(agentNC, clientID, siteID, agentID)
	st := remotesession.NewSessionTerminal(sessionID, handler, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := st.Start(ctx, terminal.ShellCmd, 80, 24); err != nil {
		t.Fatalf("SessionTerminal.Start: %v", err)
	}
	t.Cleanup(func() { st.Stop() })

	// Aguarda ready (ou envia hello)
	termIn := sessionSubject(clientID, siteID, agentID, sessionID, "term.in")
	_ = viewerNC.Publish(termIn, []byte(`{"hello":true,"cols":80,"rows":24,"pv":1}`))
	_ = viewerNC.Flush()

	// Verifica term.out sem runes UTF-8 quebrados e com seq monotonico
	var lastSeq int64 = -1
	gotOutput := false
	deadline := time.Now().Add(8 * time.Second)

	// Envia comando que gera UTF-8 acentuado
	cmdPayload, _ := json.Marshal(map[string]any{"data": "echo acao_utf8_ok\r\n", "pv": 1})
	_ = viewerNC.Publish(termIn, cmdPayload)
	_ = viewerNC.Flush()

	for time.Now().Before(deadline) {
		msg, err := termOutSub.NextMsg(500 * time.Millisecond)
		if err != nil {
			if gotOutput {
				break
			}
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal(msg.Data, &frame); err != nil {
			continue
		}
		if seqRaw, ok := frame["seq"].(float64); ok {
			seq := int64(seqRaw)
			if seq <= lastSeq {
				t.Fatalf("seq nao monotonico em term.out: prev=%d curr=%d", lastSeq, seq)
			}
			lastSeq = seq
		}
		if d, ok := frame["data"].(string); ok && d != "" {
			if !utf8.ValidString(d) {
				t.Fatalf("frame term.out base64 invalido")
			}
			gotOutput = true
		}
	}
	if !gotOutput {
		t.Fatalf("nao recebeu output em term.out")
	}

	// Testa completion.req -> completion.res
	compReq, _ := json.Marshal(map[string]any{"reqId": "c1", "input": "Get-Chi", "cursor": 7, "forward": true})
	_ = viewerNC.Publish(sessionSubject(clientID, siteID, agentID, sessionID, "completion.req"), compReq)
	_ = viewerNC.Flush()

	compMsg := waitSubMsg(t, compResSub, 8*time.Second)
	var compResp map[string]any
	if err := json.Unmarshal(compMsg.Data, &compResp); err != nil {
		t.Fatalf("completion.res JSON invalido: %v", err)
	}
	if compResp["reqId"] != "c1" {
		t.Fatalf("reqId inesperado em completion.res: %v", compResp)
	}
}
