//go:build windows

package remotesession

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestClampTermDims(t *testing.T) {
	cases := []struct {
		cols, rows         int
		wantCols, wantRows int
	}{
		{120, 40, 120, 40},
		{0, 0, termMinCols, termMinRows},
		{1, 1, termMinCols, termMinRows},
		{termMaxCols + 1, termMaxRows + 1, termMaxCols, termMaxRows},
		{2000, 1000, termMaxCols, termMaxRows},
		{termMinCols, termMinRows, termMinCols, termMinRows},
	}
	for _, c := range cases {
		gotCols, gotRows := clampTermDims(c.cols, c.rows)
		if gotCols != c.wantCols || gotRows != c.wantRows {
			t.Fatalf("clampTermDims(%d,%d) = (%d,%d), want (%d,%d)",
				c.cols, c.rows, gotCols, gotRows, c.wantCols, c.wantRows)
		}
	}
}

// makeFrame gera um payload term.out realista (base64 + seq) do tamanho pedido.
func makeFrame(seq int64, rawBytes int) string {
	raw := make([]byte, rawBytes)
	for i := range raw {
		raw[i] = byte('a' + (i % 26))
	}
	payload, _ := json.Marshal(map[string]any{
		"data": base64.StdEncoding.EncodeToString(raw),
		"seq":  seq,
	})
	return string(payload)
}

func TestTermReplayRing_ReplaysOnlyMissingFrames(t *testing.T) {
	ring := newTermReplayRing(1 << 20)
	for i := int64(0); i < 5; i++ {
		ring.add(i, makeFrame(i, 8))
	}

	frames, ok := ring.since(2)
	if !ok {
		t.Fatal("since(2) com anel completo deve ser continuável")
	}
	if len(frames) != 2 {
		t.Fatalf("since(2) devolveu %d frames, want 2", len(frames))
	}
	if frames[0].seq != 3 || frames[1].seq != 4 {
		t.Fatalf("since(2) fora de ordem: %d,%d", frames[0].seq, frames[1].seq)
	}
}

func TestTermReplayRing_ViewerEmDiaNaoRecebeNada(t *testing.T) {
	ring := newTermReplayRing(1 << 20)
	for i := int64(0); i < 3; i++ {
		ring.add(i, makeFrame(i, 8))
	}
	frames, ok := ring.since(2) // já viu o último (2)
	if !ok || len(frames) != 0 {
		t.Fatalf("since(2) = %d frames, ok=%v; want 0, true", len(frames), ok)
	}
	// lastSeq = -1 (viewer novo) recebe TUDO o que está retido.
	frames, ok = ring.since(-1)
	if !ok || len(frames) != 3 {
		t.Fatalf("since(-1) = %d frames, ok=%v; want 3, true", len(frames), ok)
	}
}

func TestTermReplayRing_AnelVazio(t *testing.T) {
	ring := newTermReplayRing(1024)
	if frames, ok := ring.since(0); !ok || len(frames) != 0 {
		t.Fatalf("anel vazio: %d frames, ok=%v; want 0, true", len(frames), ok)
	}
}

func TestTermReplayRing_DetectaLacuna(t *testing.T) {
	ring := newTermReplayRing(1 << 20)
	ring.add(10, makeFrame(10, 16))
	ring.add(11, makeFrame(11, 16))

	// O viewer parou no seq 5: os frames 6..9 já saíram do anel → não dá para
	// continuar sem emendar saída nova em saída velha (o agent manda "reset").
	if _, ok := ring.since(5); ok {
		t.Fatal("since(5) com anel começando em 10 deveria detectar lacuna")
	}
	// Fronteira contínua: 9 é exatamente o anterior ao primeiro retido (10).
	if _, ok := ring.since(9); !ok {
		t.Fatal("since(9) com anel começando em 10 é contínuo")
	}
}

func TestTermReplayRing_LimitaMemoria(t *testing.T) {
	const maxBytes = 4096
	ring := newTermReplayRing(maxBytes)
	frameSize := 512
	var seq int64
	for i := 0; i < 100; i++ {
		ring.add(seq, makeFrame(seq, frameSize/2))
		seq++
	}
	ring.mu.Lock()
	bytes, n := ring.bytes, len(ring.frames)
	first := ring.frames[0].seq
	ring.mu.Unlock()

	if bytes > maxBytes {
		t.Fatalf("anel excedeu o teto: %d > %d", bytes, maxBytes)
	}
	if n == 0 {
		t.Fatal("anel ficou vazio (deveria reter os frames mais recentes)")
	}
	// O primeiro retido deve ser recente: o seq 0 certamente foi descartado.
	if first == 0 {
		t.Fatal("frames antigos não foram descartados")
	}
	// E o mais recente continua presente.
	if _, ok := ring.since(seq - 2); !ok {
		t.Fatal("os frames mais recentes deveriam estar retidos")
	}
}

func TestTermReplayRing_Clear(t *testing.T) {
	ring := newTermReplayRing(1 << 20)
	ring.add(0, makeFrame(0, 16))
	ring.clear()
	ring.mu.Lock()
	bytes, n := ring.bytes, len(ring.frames)
	ring.mu.Unlock()
	if bytes != 0 || n != 0 {
		t.Fatalf("clear deixou bytes=%d frames=%d", bytes, n)
	}
}

func TestTermReplayRing_UmFrameMaiorQueOTetoEhMantido(t *testing.T) {
	ring := newTermReplayRing(8)
	big := makeFrame(0, 4096)
	ring.add(0, big)
	if frames, ok := ring.since(-1); !ok || len(frames) != 1 {
		t.Fatalf("um único frame acima do teto deve ser retido: %d frames, ok=%v", len(frames), ok)
	}
}

func TestTermReplayRing_Concorrencia(t *testing.T) {
	ring := newTermReplayRing(1 << 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := int64(0); i < 200; i++ {
			ring.add(i, makeFrame(i, 32))
		}
	}()
	for i := 0; i < 200; i++ {
		_, _ = ring.since(int64(i))
	}
	<-done
	// Sem race detector o valor não importa; o teste garante que add/since
	// concorrentes não entram em pânico por corrupção de slice.
	if frames, ok := ring.since(-1); !ok || len(frames) == 0 {
		t.Fatalf("estado final inesperado: %d frames, ok=%v", len(frames), ok)
	}
	_ = fmt.Sprintf("%d", len(ring.frames))
}

// ── T3: gravacao tipada ──

func TestRecordingTap_OutputFramePreservaCamposAntigos(t *testing.T) {
	f := buildTermRecordingFrame("output", "QUJD", 4, nil, nil, nil, "", "")
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["kind"] != "output" {
		t.Fatalf("kind = %v, want output", raw["kind"])
	}
	if raw["data"] != "QUJD" || raw["seq"].(float64) != 4 {
		t.Fatalf("campos antigos alterados: %v", raw)
	}
	if _, ok := raw["exitCode"]; ok {
		t.Fatal("exitCode deveria ser omitido quando nil")
	}
	if _, ok := raw["cols"]; ok {
		t.Fatal("cols deveria ser omitido quando nil")
	}
}

// TestRecordingTap_ExitFrame cobre o caminho REAL de gravacao (NATS): um frame
// kind=exit com exitCode chega ao subject recording.term.
func TestRecordingTap_ExitFrame(t *testing.T) {
	server := startLivenessNATS(t)
	nc, err := nats.Connect(server.ClientURL(), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("conectar NATS: %v", err)
	}
	t.Cleanup(nc.Close)

	h := NewNatsStreamHandler(nc, "client-1", "site-1", "agent-1")
	tap := &RecordingTap{sessionID: "sess-rec", natsStream: h, enabled: true}

	subject := h.publishSubject("sess-rec", "recording.term")
	msgs := make(chan TermRecordingFrame, 4)
	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		var f TermRecordingFrame
		if json.Unmarshal(msg.Data, &f) == nil {
			msgs <- f
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	_ = nc.Flush()

	exitCode := 3
	tap.WriteFrame("exit", "bye", 7, &exitCode, nil, nil, "conpty", "powershell")

	select {
	case f := <-msgs:
		if f.Kind != "exit" {
			t.Fatalf("kind = %q, want exit", f.Kind)
		}
		if f.ExitCode == nil || *f.ExitCode != 3 {
			t.Fatalf("exitCode = %v, want 3", f.ExitCode)
		}
		if f.Seq != 7 || f.Data != "bye" {
			t.Fatalf("seq/data = %d/%q, want 7/bye", f.Seq, f.Data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("frame de gravacao kind=exit nao chegou")
	}
}

// ── T4: contadores de telemetria ──

func TestOutputCoalescer_StatsCounters(t *testing.T) {
	stats := &termStats{startedAt: time.Now()}
	var flushed atomic.Int64
	oc := newOutputCoalescer(func(string) { flushed.Add(1) }, 3*time.Millisecond)
	oc.stats = stats

	oc.Write("hello")
	oc.ForceFlush()

	if got := stats.framesOut.Load(); got != 1 {
		t.Fatalf("framesOut = %d, want 1", got)
	}
	if got := stats.bytesOut.Load(); got != 5 {
		t.Fatalf("bytesOut = %d, want 5", got)
	}
	if got := stats.lastFlushBytes.Load(); got != 5 {
		t.Fatalf("lastFlushBytes = %d, want 5", got)
	}
	if flushed.Load() != 1 {
		t.Fatalf("flushes = %d, want 1", flushed.Load())
	}

	// Rate limit: com maxPerWindow=0 nenhuma mensagem é permitida → flush hold.
	oc2 := newOutputCoalescer(func(string) {}, time.Hour)
	oc2.stats = stats
	oc2.maxPerWindow = 0
	oc2.Write("x")
	oc2.flush()
	if got := stats.flushHolds.Load(); got != 1 {
		t.Fatalf("flushHolds = %d, want 1", got)
	}

	// Teto de buffer → forceDispatchLocked (ignora rate limit).
	oc3 := newOutputCoalescer(func(string) {}, time.Hour)
	oc3.stats = stats
	oc3.Write(string(make([]byte, maxCoalesceBufferBytes)))
	if got := stats.flushForcedAtCap.Load(); got != 1 {
		t.Fatalf("flushForcedAtCap = %d, want 1", got)
	}

	snap := stats.snapshot("sess-x")
	if snap.SessionID != "sess-x" || snap.FramesOut < 1 || snap.BytesOut < 5 {
		t.Fatalf("snapshot inesperado: %+v", snap)
	}
	if snap.AvgFlushBytes != snap.BytesOut/snap.FramesOut {
		t.Fatalf("avgFlushBytes = %d, want %d", snap.AvgFlushBytes, snap.BytesOut/snap.FramesOut)
	}
	if snap.UptimeMs < 0 {
		t.Fatalf("uptimeMs negativo: %d", snap.UptimeMs)
	}
}

// ── T5: capacidades no term.ready ──

func TestBuildTermReadyPayload_Capabilities(t *testing.T) {
	ready := buildTermReadyPayload([]string{"powershell", "cmd"}, "main", 120, 40, "powershell", `C:pwsh.exe`, "conpty")

	// Campos antigos preservados (não quebrar consumidores atuais).
	for _, k := range []string{"shells", "consoleId", "termCols", "termRows", "backend"} {
		if _, ok := ready[k]; !ok {
			t.Fatalf("campo antigo %q ausente do ready", k)
		}
	}
	if ready["capabilitiesVersion"] != 1 {
		t.Fatalf("capabilitiesVersion = %v, want 1", ready["capabilitiesVersion"])
	}
	if ready["shellKind"] != "powershell" || ready["shellPath"] != `C:pwsh.exe` {
		t.Fatalf("shellKind/shellPath errados: %v", ready)
	}
	if ready["encoding"] != "utf-8" {
		t.Fatalf("encoding = %v, want utf-8", ready["encoding"])
	}
	if ready["supportsVt"] != true || ready["supportsResize"] != true || ready["supportsCompletionQuery"] != true {
		t.Fatalf("conpty+powershell deveria suportar VT/resize/completion: %v", ready)
	}

	// Legacy NAO tem VT nem completion, mas redimensiona o console real
	// (AttachConsole + SetConsoleScreenBufferSize) — R6.
	legacy := buildTermReadyPayload(nil, "main", 80, 25, "cmd", `C:cmd.exe`, "legacy")
	if legacy["supportsVt"] != false || legacy["supportsResize"] != true || legacy["supportsCompletionQuery"] != false {
		t.Fatalf("legacy: VT=false, resize=true, completion=false esperados: %v", legacy)
	}
}

// ── T1: tratamento de completion ──

func TestBuildCompletionResponse_InvalidPayload(t *testing.T) {
	st := &SessionTerminal{sessionID: "sess-c"}
	out := st.buildCompletionResponse([]byte("{nao-e-json"))

	var res completionRes
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("resposta deve ser JSON valido: %v (%s)", err, out)
	}
	if res.Ok {
		t.Fatal("payload invalido deveria retornar ok=false")
	}
	if res.Error == "" {
		t.Fatal("payload invalido deveria trazer error explicito")
	}
}

func TestBuildCompletionResponse_UnsupportedShell(t *testing.T) {
	st := &SessionTerminal{sessionID: "sess-c", termShellKind: "cmd", termBackend: "conpty"}
	out := st.buildCompletionResponse([]byte(`{"reqId":"r1","input":"Get-","cursor":4}`))

	var res completionRes
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("resposta deve ser JSON valido: %v", err)
	}
	if res.Ok || res.Error == "" {
		t.Fatalf("shell nao-powershell deveria retornar ok=false com erro; got %+v", res)
	}
}

func TestPaginateCompletion(t *testing.T) {
	result := tabExpansionResult{
		ReplacementIndex:  2,
		ReplacementLength: 1,
		Matches:           []completionMatch{{Text: "a"}, {Text: "b"}, {Text: "c"}},
	}
	res := paginateCompletion(completionReq{ReqID: "r", Page: 0, PageSize: 2}, result)
	if !res.Ok || len(res.Matches) != 2 || res.TotalCount != 3 || !res.HasMore {
		t.Fatalf("pagina 0 inesperada: %+v", res)
	}
	if res.ReplacementIndex != 2 || res.ReplacementLength != 1 {
		t.Fatalf("metadados de replacement perdidos: %+v", res)
	}
	res2 := paginateCompletion(completionReq{ReqID: "r", Page: 1, PageSize: 2}, result)
	if len(res2.Matches) != 1 || res2.HasMore {
		t.Fatalf("pagina 1 inesperada: %+v", res2)
	}
	res3 := paginateCompletion(completionReq{ReqID: "r", Page: 99, PageSize: 2}, result)
	if len(res3.Matches) != 0 || res3.HasMore {
		t.Fatalf("pagina fora do range deveria ser vazia: %+v", res3)
	}
}
