//go:build windows

package remotesession

import (
	"strings"
	"testing"

	"discovery/app/core/sessioncontrol"
)

func TestDecodeRemoteSessionControl_TypedEnvelope(t *testing.T) {
	raw, err := sessioncontrol.Encode(sessioncontrol.Envelope{
		Type:      ControlTypePing,
		SessionID: "abc",
		From:      sessioncontrol.RoleViewer,
	}, sessioncontrol.RemoteSessionTypes)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	env, err := DecodeRemoteSessionControl(raw)
	if err != nil {
		t.Fatalf("DecodeRemoteSessionControl: %v", err)
	}
	if env.Type != ControlTypePing || env.From != sessioncontrol.RoleViewer || env.SessionID != "abc" {
		t.Fatalf("envelope inesperado: %+v", env)
	}
}

func TestDecodeRemoteSessionControl_LegacyKeyframe(t *testing.T) {
	env, err := DecodeRemoteSessionControl([]byte("{\"action\":\"keyframe\"}"))
	if err != nil {
		t.Fatalf("legado deveria ser aceito: %v", err)
	}
	if env.Type != ControlTypeKeyframe || env.From != sessioncontrol.RoleViewer {
		t.Fatalf("legado inesperado: %+v", env)
	}
	if env.SessionID != "" {
		t.Fatalf("legado nao carrega sessionId, got %q", env.SessionID)
	}
}

func TestDecodeRemoteSessionControl_RejectsUnknownAndGarbage(t *testing.T) {
	cases := map[string]string{
		"tipo desconhecido": "{\"v\":1,\"type\":\"shutdown\",\"sessionId\":\"abc\",\"from\":\"viewer\"}",
		"from invalido":     "{\"v\":1,\"type\":\"ping\",\"sessionId\":\"abc\",\"from\":\"hacker\"}",
		"sem sessionId":     "{\"v\":1,\"type\":\"ping\",\"from\":\"viewer\"}",
		"json quebrado":     "{nope",
		"payload vazio":     "",
		"grande demais":     "{\"action\":\"keyframe\",\"pad\":\"" + strings.Repeat("x", 600) + "\"}",
	}
	for name, payload := range cases {
		if _, err := DecodeRemoteSessionControl([]byte(payload)); err == nil {
			t.Fatalf("%s deveria ser rejeitado", name)
		}
	}
}

func TestEncodeRemoteSessionControl_AllowList(t *testing.T) {
	raw, err := encodeRemoteSessionControl(sessioncontrol.NewEnvelope(
		sessioncontrol.RoleAgent, ControlTypePong, "abc", 1, nil))
	if err != nil {
		t.Fatalf("pong deveria ser codificavel: %v", err)
	}
	if !strings.Contains(string(raw), "\"type\":\"pong\"") {
		t.Fatalf("payload inesperado: %s", raw)
	}
	if _, err := encodeRemoteSessionControl(sessioncontrol.NewEnvelope(
		sessioncontrol.RoleAgent, "setLevel", "abc", 1, nil)); err == nil {
		t.Fatalf("setLevel (debug) nao pertence ao dominio da sessao remota")
	}
}

func TestParseLivenessConfig_DefaultsAndOverrides(t *testing.T) {
	got := parseLivenessConfig(nil)
	if got.PingIntervalSeconds != 5 || got.MissedPingsBeforeClose != 3 || got.InitialGraceSeconds != 60 {
		t.Fatalf("defaults inesperados: %+v", got)
	}

	got = parseLivenessConfig(map[string]any{
		"pingIntervalSeconds":    2,
		"missedPingsBeforeClose": 4,
		"initialGraceSeconds":    15,
	})
	if got.PingIntervalSeconds != 2 || got.MissedPingsBeforeClose != 4 || got.InitialGraceSeconds != 15 {
		t.Fatalf("overrides inesperados: %+v", got)
	}

	got = parseLivenessConfig(map[string]any{"pingIntervalSeconds": 0, "missedPingsBeforeClose": -1})
	if got.PingIntervalSeconds != 5 || got.MissedPingsBeforeClose != 3 {
		t.Fatalf("valores invalidos deveriam manter defaults: %+v", got)
	}
}
