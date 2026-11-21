package remotesession

import (
	"encoding/json"
	"testing"
)

// A sinalização WebRTC é o caminho onde uma falha silenciosa é mais cara: se um
// frame malformado deixar o peer connection pendurado, o operador vê
// "conectando..." sem causa. Estes testes fixam o comportamento de forma,
// geração e política de falha.

// ── DecodeSignalFrame ───────────────────────────────────────────────────

func TestDecodeSignalFrame_OfferValida(t *testing.T) {
	raw := []byte(`{"kind":"offer","sdp":"v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\n"}`)

	frame, err := DecodeSignalFrame(raw)
	if err != nil {
		t.Fatalf("oferta válida rejeitada: %v", err)
	}
	if frame.Kind != SignalKindOffer {
		t.Fatalf("kind = %q, esperava %q", frame.Kind, SignalKindOffer)
	}
	if frame.SDP == "" {
		t.Fatal("SDP não preservado")
	}
}

func TestDecodeSignalFrame_CandidateValido(t *testing.T) {
	raw := []byte(`{"kind":"candidate","candidate":"candidate:1 1 udp 2113937151 192.168.1.5 50000 typ host","generation":3}`)

	frame, err := DecodeSignalFrame(raw)
	if err != nil {
		t.Fatalf("candidato válido rejeitado: %v", err)
	}
	if frame.Generation != 3 {
		t.Fatalf("generation = %d, esperava 3", frame.Generation)
	}
}

func TestDecodeSignalFrame_RejeitaVazios(t *testing.T) {
	cases := map[string][]byte{
		"frame vazio":       {},
		"json inválido":     []byte("{nao-e-json"),
		"kind ausente":      []byte(`{"sdp":"v=0"}`),
		"kind desconhecido": []byte(`{"kind":"teleporte"}`),
		"oferta sem sdp":    []byte(`{"kind":"offer"}`),
		"candidato vazio":   []byte(`{"kind":"candidate","candidate":"   "}`),
	}

	for name, raw := range cases {
		if _, err := DecodeSignalFrame(raw); err == nil {
			t.Fatalf("%s: esperava erro de forma", name)
		}
	}
}

func TestEncodeSignalFrame_RoundTrip(t *testing.T) {
	raw, err := EncodeSignalFrame(SignalFrame{Kind: SignalKindAnswer, SDP: "v=0"})
	if err != nil {
		t.Fatalf("encode falhou: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json inválido: %v", err)
	}
	if decoded["kind"] != SignalKindAnswer {
		t.Fatalf("kind = %v", decoded["kind"])
	}
}

// ── WebRtcSession: geração ──────────────────────────────────────────────

func TestWebRtcSession_CandidatoDeGeracaoAntigaEhDescartado(t *testing.T) {
	// Envenenar a negociação corrente com um candidato de uma tentativa anterior
	// é o bug que a geração existe para evitar.
	var applied []string

	policy := WebRtcPolicy{Enabled: true, FallbackToNats: true}
	ws := NewWebRtcSession("sess-1", policy,
		func(string) error { return nil },
		func(c string) error { applied = append(applied, c); return nil },
		func(string) {},
		func(string) {},
	)

	// Leva a geração a 1 submetendo uma oferta (que falhará na negociação, mas
	// já incrementou a geração).
	offer, _ := EncodeSignalFrame(SignalFrame{Kind: SignalKindOffer, SDP: "v=0"})
	_ = ws.HandleFrame(offer)

	if ws.Generation() != 1 {
		t.Fatalf("generation = %d, esperava 1 após a oferta", ws.Generation())
	}

	// Candidato da geração 0 (anterior) deve ser IGNORADO. O zero NÃO é curinga:
	// tratá-lo como "aceita qualquer" deixaria o candidato de uma negociação
	// anterior entrar na corrente — exatamente o bug que a geração evita.
	stale, _ := EncodeSignalFrame(SignalFrame{Kind: SignalKindCandidate, Candidate: "candidate:antigo", Generation: 0})
	if err := ws.HandleFrame(stale); err != nil {
		t.Fatalf("candidato de geração antiga não deveria falhar: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("candidato de geração antiga foi aplicado: %v", applied)
	}

	// Candidato da geração corrente é aplicado.
	current, _ := EncodeSignalFrame(SignalFrame{Kind: SignalKindCandidate, Candidate: "candidate:atual", Generation: 1})
	if err := ws.HandleFrame(current); err != nil {
		t.Fatalf("candidato da geração corrente falhou: %v", err)
	}
	if len(applied) != 1 || applied[0] != "candidate:atual" {
		t.Fatalf("candidato da geração corrente não aplicado: %v", applied)
	}
}

func TestWebRtcSession_NovaOfertaIncrementaGeracao(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true, FallbackToNats: true}
	ws := NewWebRtcSession("sess-2", policy,
		func(string) error { return nil },
		func(string) error { return nil },
		func(string) {},
		func(string) {},
	)

	offer, _ := EncodeSignalFrame(SignalFrame{Kind: SignalKindOffer, SDP: "v=0"})

	_ = ws.HandleFrame(offer)
	_ = ws.HandleFrame(offer)
	_ = ws.HandleFrame(offer)

	if ws.Generation() != 3 {
		t.Fatalf("generation = %d, esperava 3", ws.Generation())
	}
}

// ── WebRtcSession: política de falha ────────────────────────────────────

func TestWebRtcSession_FalhaComFallbackAcionaFallback(t *testing.T) {
	var fallbackReason string
	var fatalCalled bool

	policy := WebRtcPolicy{Enabled: true, FallbackToNats: true}
	ws := NewWebRtcSession("sess-3", policy,
		func(string) error { return nil },
		func(string) error { return nil },
		func(reason string) { fallbackReason = reason },
		func(string) { fatalCalled = true },
	)

	// A negociação não está ligada (stack de mídia ausente), então a oferta
	// falha. Com fallback ligado, a sessão CONTINUA pelo NATS.
	offer, _ := EncodeSignalFrame(SignalFrame{Kind: SignalKindOffer, SDP: "v=0"})
	_ = ws.HandleFrame(offer)

	if fallbackReason == "" {
		t.Fatal("esperava fallback acionado")
	}
	if fatalCalled {
		t.Fatal("fallback ligado não pode encerrar a sessão")
	}
}

func TestWebRtcSession_FalhaSemFallbackEncerra(t *testing.T) {
	var fatalReason string
	var fallbackCalled bool

	policy := WebRtcPolicy{Enabled: true, FallbackToNats: false}
	ws := NewWebRtcSession("sess-4", policy,
		func(string) error { return nil },
		func(string) error { return nil },
		func(string) { fallbackCalled = true },
		func(reason string) { fatalReason = reason },
	)

	offer, _ := EncodeSignalFrame(SignalFrame{Kind: SignalKindOffer, SDP: "v=0"})
	_ = ws.HandleFrame(offer)

	if fatalReason == "" {
		t.Fatal("esperava encerramento com motivo explícito")
	}
	if fallbackCalled {
		t.Fatal("fallback desligado não pode acionar o fallback")
	}
}

func TestWebRtcSession_ErroDoViewerAcionaFallback(t *testing.T) {
	var fallbackReason string

	policy := WebRtcPolicy{Enabled: true, FallbackToNats: true}
	ws := NewWebRtcSession("sess-5", policy,
		func(string) error { return nil },
		func(string) error { return nil },
		func(reason string) { fallbackReason = reason },
		func(string) {},
	)

	errFrame, _ := EncodeSignalFrame(SignalFrame{Kind: SignalKindError, Reason: "ice failed"})
	_ = ws.HandleFrame(errFrame)

	if fallbackReason == "" {
		t.Fatal("erro reportado pelo viewer deveria acionar o fallback")
	}
	if !contains(fallbackReason, "ice failed") {
		t.Fatalf("motivo não propagado: %q", fallbackReason)
	}
}

func TestWebRtcSession_FrameInvalidoNaoEncerraSessao(t *testing.T) {
	// Um frame malformado é transitório: não pode derrubar a sessão, apenas
	// acionar o fallback (quando permitido).
	var fatalCalled bool

	policy := WebRtcPolicy{Enabled: true, FallbackToNats: true}
	ws := NewWebRtcSession("sess-6", policy,
		func(string) error { return nil },
		func(string) error { return nil },
		func(string) {},
		func(string) { fatalCalled = true },
	)

	if err := ws.HandleFrame([]byte("{nao-e-json")); err == nil {
		t.Fatal("esperava erro de forma")
	}
	if fatalCalled {
		t.Fatal("frame malformado não pode encerrar a sessão quando há fallback")
	}
}

// ── WebRtcSession: Close ────────────────────────────────────────────────

func TestWebRtcSession_FechadaIgnoraFramesTardios(t *testing.T) {
	var applied bool

	policy := WebRtcPolicy{Enabled: true, FallbackToNats: true}
	ws := NewWebRtcSession("sess-7", policy,
		func(string) error { return nil },
		func(string) error { applied = true; return nil },
		func(string) {},
		func(string) {},
	)

	ws.Close()

	candidate, _ := EncodeSignalFrame(SignalFrame{Kind: SignalKindCandidate, Candidate: "candidate:tardio"})
	if err := ws.HandleFrame(candidate); err != nil {
		t.Fatalf("frame em sessão fechada não deveria falhar: %v", err)
	}
	if applied {
		t.Fatal("sessão fechada não pode aplicar candidatos")
	}
}

// ── resolveWebRtcTransport ──────────────────────────────────────────────

func TestResolveWebRtcTransport_TransporteNaoWebRtcNaoUsa(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true}
	for _, transport := range []string{"nats", "http", ""} {
		use, _ := resolveWebRtcTransport(transport, "screen", policy)
		if use {
			t.Fatalf("transporte %q não deveria usar WebRTC", transport)
		}
	}
}

func TestResolveWebRtcTransport_PoliticaDesligadaNaoUsa(t *testing.T) {
	policy := disabledWebRtcPolicy()
	use, reason := resolveWebRtcTransport("webrtc", "screen", policy)

	if use {
		t.Fatal("política desligada não pode usar WebRTC")
	}
	if reason == "" {
		t.Fatal("esperava motivo explícito para o indicador")
	}
}

func TestResolveWebRtcTransport_RelayOnlySemTurnNaoUsa(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"kinds":      []any{"screen"},
		"relayOnly":  true,
		"iceServers": []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	use, reason := resolveWebRtcTransport("webrtc", "screen", policy)
	if use {
		t.Fatal("relayOnly sem TURN não pode usar WebRTC (a sessão nunca conectaria)")
	}
	if !contains(reason, "TURN") {
		t.Fatalf("motivo deveria citar TURN: %q", reason)
	}
}

func TestResolveWebRtcTransport_KindForaDaListaNaoUsa(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"kinds":      []any{"screen"},
		"iceServers": []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	use, reason := resolveWebRtcTransport("webrtc", "terminal", policy)
	if use {
		t.Fatal("terminal não está na lista e não pode usar WebRTC")
	}
	if !contains(reason, "terminal") {
		t.Fatalf("motivo deveria citar o kind: %q", reason)
	}
}

func TestResolveWebRtcTransport_ConfiguracaoValidaUsa(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"kinds":      []any{"screen", "files"},
		"iceServers": []any{map[string]any{"urls": []any{"stun:stun.l.google.com:19302"}}},
	})

	use, reason := resolveWebRtcTransport("webrtc", "screen", policy)
	if !use {
		t.Fatalf("configuração válida deveria usar WebRTC, recusou por: %q", reason)
	}

	// Arquivos também é autorizado nesta política.
	if use, _ := resolveWebRtcTransport("webrtc", "files", policy); !use {
		t.Fatal("files está na lista e deveria ser autorizado")
	}
}

// ── TransportInfo ───────────────────────────────────────────────────────

func TestTransportInfo_EstadoInicialNats(t *testing.T) {
	session := &Session{ID: "s1", Kind: "screen", Transport: "nats"}

	info := session.TransportInfo()
	if info.State != TransportStateNats || info.Active != TransportStateNats {
		t.Fatalf("estado = %q/%q, esperava nats/nats", info.State, info.Active)
	}
}

func TestTransportInfo_WebRtcAutorizado(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true, RelayOnly: false}
	session := &Session{ID: "s2", Kind: "screen", Transport: "webrtc", WebRtcPolicy: &policy}
	session.SetTransportState(TransportStateWebRtc, "")

	info := session.TransportInfo()
	if info.State != TransportStateWebRtc || info.Active != "webrtc" {
		t.Fatalf("estado = %q/%q, esperava webrtc/webrtc", info.State, info.Active)
	}
	if info.Requested != "webrtc" {
		t.Fatalf("requested = %q", info.Requested)
	}
}

func TestTransportInfo_FallbackPreservaMotivo(t *testing.T) {
	// O caso que o operador MAIS precisa ver: a sessão funciona, mas não pelo
	// caminho que ele configurou.
	policy := WebRtcPolicy{Enabled: true}
	session := &Session{ID: "s3", Kind: "screen", Transport: "webrtc", WebRtcPolicy: &policy}
	session.SetTransportState(TransportStateFallback, "ICE falhou: timeout")

	info := session.TransportInfo()
	if info.State != TransportStateFallback {
		t.Fatalf("state = %q, esperava %q", info.State, TransportStateFallback)
	}
	if info.Active != "nats" {
		t.Fatalf("active = %q, esperava nats (a mídia voltou para o NATS)", info.Active)
	}
	if info.Reason != "ICE falhou: timeout" {
		t.Fatalf("reason = %q", info.Reason)
	}
}

func TestTransportInfo_SerializaParaOConsola(t *testing.T) {
	policy := WebRtcPolicy{Enabled: true}
	session := &Session{ID: "s4", Kind: "screen", Transport: "webrtc", WebRtcPolicy: &policy}
	session.SetTransportState(TransportStateFallback, "sem TURN")

	raw, err := json.Marshal(session.TransportInfo())
	if err != nil {
		t.Fatalf("marshal falhou: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json inválido: %v", err)
	}

	// O console depende destas chaves em camelCase.
	for _, key := range []string{"state", "requested", "active", "reason", "hasStun", "hasTurn", "relayOnly"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("chave %q ausente do JSON do indicador", key)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 ||
		indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
