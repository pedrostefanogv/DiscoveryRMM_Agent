//go:build windows

package remotesession

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"discovery/app/core/sessioncontrol"
)

// O selo do console só existe se o frame de transport_info sobreviver ao codec
// do canal .control.
//
// Estes testes fixam o contrato que estava QUEBRADO em produção: o agent
// publicava {"action":"transport_info",...} sem o campo "type", o decoder do
// console exige "type" (decodeRemoteControlEnvelope), e o resultado era o selo
// preso em "NATS" com o peer connection "connected" e os data channels abertos.

func TestRemoteSessionTypes_AceitaTransportInfoEWeRtcReady(t *testing.T) {
	for _, typ := range []string{ControlTypeTransportInfo, ControlTypeWebRtcReady} {
		if !sessioncontrol.RemoteSessionTypes.Has(typ) {
			t.Fatalf("tipo %q fora da allow-list: o codec recusaria o frame na CODIFICACAO", typ)
		}
	}
}

func TestTransportInfo_EnvelopeTipadoSobreviveAoCodec(t *testing.T) {
	info := TransportInfo{
		State:     TransportStateWebRtc,
		Requested: "webrtc",
		Active:    "webrtc",
		HasStun:   true,
	}
	env := sessioncontrol.NewEnvelope(
		sessioncontrol.RoleAgent,
		ControlTypeTransportInfo,
		"2337998e-a5f2-415d-a2c2-4ca377b0e867",
		7,
		map[string]any{"transport": info},
	)

	raw, err := encodeRemoteSessionControl(env)
	if err != nil {
		t.Fatalf("transporte nao pode ser codificado (era exatamente o bug do selo): %v", err)
	}
	if len(raw) > sessioncontrol.MaxEnvelopeBytes {
		t.Fatalf("envelope de %d bytes excede o teto de %d", len(raw), sessioncontrol.MaxEnvelopeBytes)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("envelope nao e JSON: %v", err)
	}
	// O campo "type" e o que o decoder do console exige; sem ele o frame era
	// descartado ANTES de chegar ao selo.
	if decoded["type"] != ControlTypeTransportInfo {
		t.Fatalf("campo type = %v, esperava %q", decoded["type"], ControlTypeTransportInfo)
	}
	if _, legacy := decoded["action"]; legacy {
		t.Fatal("envelope tipado nao deve carregar o campo legado action")
	}

	back, err := DecodeRemoteSessionControl(raw)
	if err != nil {
		t.Fatalf("decodificacao falhou: %v", err)
	}
	if back.Type != ControlTypeTransportInfo {
		t.Fatalf("type = %q", back.Type)
	}
	transport, ok := back.Payload["transport"].(map[string]any)
	if !ok {
		t.Fatal("payload.transport ausente apos o round-trip")
	}
	if transport["state"] != TransportStateWebRtc {
		t.Fatalf("state = %v, esperava %q", transport["state"], TransportStateWebRtc)
	}
}

func TestTransportInfo_FallbackPreservaMotivoNoPayload(t *testing.T) {
	info := TransportInfo{
		State:     TransportStateFallback,
		Requested: "webrtc",
		Active:    TransportStateNats,
		Reason:    "conexao WebRTC falhou (ICE nao estabeleceu)",
	}
	raw, err := encodeRemoteSessionControl(sessioncontrol.NewEnvelope(
		sessioncontrol.RoleAgent,
		ControlTypeTransportInfo,
		"2337998e-a5f2-415d-a2c2-4ca377b0e867",
		8,
		map[string]any{"transport": info},
	))
	if err != nil {
		t.Fatalf("codificacao falhou: %v", err)
	}

	back, err := DecodeRemoteSessionControl(raw)
	if err != nil {
		t.Fatalf("decodificacao falhou: %v", err)
	}
	transport := back.Payload["transport"].(map[string]any)
	// O motivo NAO e decorativo: e como o operador descobre que a sessao esta
	// em NATS apesar de ele ter configurado WebRTC.
	if transport["reason"] != info.Reason {
		t.Fatalf("reason = %v, esperava %q", transport["reason"], info.Reason)
	}
}

// O envelope de controle RECUSA o frame INTEIRO acima de 512 B. Como o "reason"
// vem do VIEWER (frame de sinalizacao de erro) e nao tem tamanho garantido, sem
// limite o selo ficaria CONGELADO no ultimo estado conhecido — e justamente no
// caminho de fallback, que e quando ele mais importa ao operador.
func TestTransportInfo_MotivoLongoNaoCongelaOSelo(t *testing.T) {
	info := TransportInfo{
		State:     TransportStateFallback,
		Requested: "webrtc",
		Active:    TransportStateNats,
		Reason:    strings.Repeat("x", 4000),
	}

	// 1. O frame integral NAO cabe — e a prova de que o limite era necessario.
	if _, err := encodeTransportInfoFrame("sess-1", info, 1); err == nil {
		t.Fatal("esperava que o frame integral estourasse o teto de 512 B")
	}

	// 2. O frame limitado cabe e PRESERVA o estado e o transporte ativo.
	bounded := boundTransportInfo(info)
	raw, err := encodeTransportInfoFrame("sess-1", bounded, 1)
	if err != nil {
		t.Fatalf("frame limitado deveria caber: %v", err)
	}
	back, err := DecodeRemoteSessionControl(raw)
	if err != nil {
		t.Fatalf("decodificacao falhou: %v", err)
	}
	transport := back.Payload["transport"].(map[string]any)
	if transport["state"] != TransportStateFallback {
		t.Fatalf("state = %v, esperava %q", transport["state"], TransportStateFallback)
	}
	if transport["active"] != TransportStateNats {
		t.Fatalf("active = %v, esperava %q", transport["active"], TransportStateNats)
	}
}

// "requested" vem do payload de start e tambem nao tem tamanho garantido.
func TestTransportInfo_RequestedNaoConfiavelTambemECortado(t *testing.T) {
	info := TransportInfo{
		State:     TransportStateNats,
		Requested: strings.Repeat("w", 4000),
		Active:    TransportStateNats,
	}
	if _, err := encodeTransportInfoFrame("sess-1", boundTransportInfo(info), 1); err != nil {
		t.Fatalf("frame com requested limitado deveria caber: %v", err)
	}
}

// Cortar por BYTES produziria UTF-8 invalido e, portanto, JSON invalido.
func TestClampText_NaoCortaUTF8AoMeio(t *testing.T) {
	s := strings.Repeat("ç", 10)
	got := clampText(s, 3)
	if !utf8.ValidString(got) {
		t.Fatalf("clampText produziu UTF-8 invalido: %q", got)
	}
	if r := []rune(got); len(r) < 3 || r[0] != 'ç' {
		t.Fatalf("clampText corrompeu o texto: %q", got)
	}
}

// O motivo do viewer era propagado integralmente para o log e para o envelope.
func TestDecodeSignalFrame_MotivoDoViewerEhLimitado(t *testing.T) {
	raw := []byte(`{"kind":"error","reason":"` + strings.Repeat("x", 5000) + `"}`)
	frame, err := DecodeSignalFrame(raw)
	if err != nil {
		t.Fatalf("frame de erro valido rejeitado: %v", err)
	}
	if got := len([]rune(frame.Reason)); got > maxSignalReasonChars+3 {
		t.Fatalf("reason nao foi limitado: %d runes", got)
	}
}

func TestWebRtcReady_ViewerAceitoPeloDecoderDoAgente(t *testing.T) {
	// O handshake e emitido pelo VIEWER; o agent precisa aceita-lo.
	env := sessioncontrol.NewEnvelope(
		sessioncontrol.RoleViewer,
		ControlTypeWebRtcReady,
		"sess-1",
		1,
		nil,
	)
	raw, err := sessioncontrol.Encode(env, sessioncontrol.RemoteSessionTypes)
	if err != nil {
		t.Fatalf("webrtcReady do viewer recusado na codificacao: %v", err)
	}

	back, err := DecodeRemoteSessionControl(raw)
	if err != nil {
		t.Fatalf("agent recusou webrtcReady: %v", err)
	}
	if back.Type != ControlTypeWebRtcReady {
		t.Fatalf("type = %q, esperava %q", back.Type, ControlTypeWebRtcReady)
	}
	if back.From != sessioncontrol.RoleViewer {
		t.Fatalf("from = %q, esperava viewer", back.From)
	}
}
