package sessioncontrol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMaxEnvelopeBytesConstant(t *testing.T) {
	if MaxEnvelopeBytes != 512 {
		t.Fatalf("MaxEnvelopeBytes = %d, want 512", MaxEnvelopeBytes)
	}
}

func TestTypeSetAllowlistsAndNilSafety(t *testing.T) {
	var nilSet TypeSet
	if nilSet.Has("ping") {
		t.Fatal("nil TypeSet should return false")
	}

	for _, allowed := range []string{"ping", "pong", "setLevel", "levelChanged", "closed"} {
		if !RemoteDebugTypes.Has(allowed) {
			t.Errorf("RemoteDebugTypes missing %q", allowed)
		}
	}
	for _, disallowed := range []string{"keyframe", "inputLock", "inputLockChanged", "", "unknown"} {
		if RemoteDebugTypes.Has(disallowed) {
			t.Errorf("RemoteDebugTypes unexpectedly allows %q", disallowed)
		}
	}

	for _, allowed := range []string{"ping", "pong", "keyframe", "closed", "inputLock", "inputLockChanged"} {
		if !RemoteSessionTypes.Has(allowed) {
			t.Errorf("RemoteSessionTypes missing %q", allowed)
		}
	}
	for _, disallowed := range []string{"setLevel", "levelChanged", "", "exec", "unknown"} {
		if RemoteSessionTypes.Has(disallowed) {
			t.Errorf("RemoteSessionTypes unexpectedly allows %q", disallowed)
		}
	}
}

func TestIsRole(t *testing.T) {
	for _, role := range []string{RoleViewer, RoleAgent, RoleServer} {
		if !IsRole(role) {
			t.Errorf("IsRole(%q) = false, want true", role)
		}
	}
	for _, bad := range []string{"", "admin", "VIEWER", "system", " viewer "} {
		if IsRole(bad) {
			t.Errorf("IsRole(%q) = true, want false", bad)
		}
	}
}

func TestNewEnvelopeAndEncodeDecodeRoundTrip(t *testing.T) {
	env := NewEnvelope(RoleViewer, "inputLock", "sess-123", 42, map[string]any{"locked": true})
	if env.Version != 1 {
		t.Fatalf("Version = %d, want 1", env.Version)
	}
	if _, err := time.Parse(time.RFC3339Nano, env.TimestampUTC); err != nil {
		t.Fatalf("TimestampUTC %q invalid RFC3339Nano: %v", env.TimestampUTC, err)
	}

	raw, err := Encode(env, RemoteSessionTypes)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}
	if len(raw) > MaxEnvelopeBytes {
		t.Fatalf("encoded frame length %d > %d", len(raw), MaxEnvelopeBytes)
	}

	decoded, err := Decode(raw, RemoteSessionTypes)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if decoded.Version != 1 || decoded.Type != "inputLock" || decoded.SessionID != "sess-123" || decoded.From != RoleViewer || decoded.Sequence != 42 {
		t.Fatalf("unexpected decoded envelope: %+v", decoded)
	}
	if got, ok := decoded.Payload["locked"].(bool); !ok || !got {
		t.Fatalf("decoded payload mismatch: %+v", decoded.Payload)
	}
}

func TestEncodeValidation(t *testing.T) {
	// Type not in allowlist must fail.
	_, err := Encode(Envelope{From: RoleAgent, Type: "setLevel", SessionID: "s1"}, RemoteSessionTypes)
	if err == nil || !strings.Contains(err.Error(), "tipo de controle invalido") {
		t.Fatalf("expected invalid type error, got %v", err)
	}

	// Zero version defaults to 1 on wire.
	raw, err := Encode(Envelope{From: RoleAgent, Type: "ping", SessionID: "s1"}, RemoteDebugTypes)
	if err != nil {
		t.Fatalf("Encode with zero version failed: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if wire["v"] != float64(1) {
		t.Fatalf("wire version = %v, want 1", wire["v"])
	}

	// Oversized payload > MaxEnvelopeBytes must fail.
	oversized := NewEnvelope(RoleAgent, "ping", "s1", 1, map[string]any{
		"blob": strings.Repeat("x", MaxEnvelopeBytes),
	})
	_, err = Encode(oversized, RemoteDebugTypes)
	if err == nil || !strings.Contains(err.Error(), "excede") {
		t.Fatalf("expected oversized envelope error, got %v", err)
	}

	// Invalid sessionIds (empty, control chars, internal whitespace, wildcards) must fail in Encode.
	for _, badID := range []string{"", "   ", "sess\x00id", "sess\x1fid", "sess\x7fid", "sess id", "sess\tid", "sess*", "sess.>"} {
		if _, err := Encode(NewEnvelope(RoleAgent, "ping", badID, 1, nil), RemoteDebugTypes); err == nil {
			t.Fatalf("Encode should reject invalid sessionId %q", badID)
		}
	}
}

func TestDecodeValidationRejectsInvalidFrames(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		allowed TypeSet
		wantErr string
	}{
		{
			name:    "empty slice",
			raw:     []byte{},
			allowed: RemoteSessionTypes,
			wantErr: "vazio",
		},
		{
			name:    "nil slice",
			raw:     nil,
			allowed: RemoteSessionTypes,
			wantErr: "vazio",
		},
		{
			name:    "exceeds max envelope bytes",
			raw:     []byte(strings.Repeat("a", MaxEnvelopeBytes+1)),
			allowed: RemoteSessionTypes,
			wantErr: "excede",
		},
		{
			name:    "malformed json",
			raw:     []byte(`{"type":"ping"`),
			allowed: RemoteSessionTypes,
			wantErr: "JSON",
		},
		{
			name:    "type outside allowlist",
			raw:     []byte(`{"v":1,"type":"setLevel","from":"viewer","sessionId":"s1"}`),
			allowed: RemoteSessionTypes,
			wantErr: "tipo de controle invalido",
		},
		{
			name:    "missing from",
			raw:     []byte(`{"v":1,"type":"ping","sessionId":"s1"}`),
			allowed: RemoteSessionTypes,
			wantErr: "from invalido",
		},
		{
			name:    "unknown from role",
			raw:     []byte(`{"v":1,"type":"ping","from":"root","sessionId":"s1"}`),
			allowed: RemoteSessionTypes,
			wantErr: "from invalido",
		},
		{
			name:    "missing sessionId",
			raw:     []byte(`{"v":1,"type":"ping","from":"viewer"}`),
			allowed: RemoteSessionTypes,
			wantErr: "sessionId ausente",
		},
		{
			name:    "whitespace-only sessionId",
			raw:     []byte(`{"v":1,"type":"ping","from":"viewer","sessionId":"  \t\n "}`),
			allowed: RemoteSessionTypes,
			wantErr: "sessionId ausente",
		},
		{
			name:    "control byte nul in sessionId",
			raw:     []byte("{\"v\":1,\"type\":\"ping\",\"from\":\"viewer\",\"sessionId\":\"sess\\u0000id\"}"),
			allowed: RemoteSessionTypes,
			wantErr: "sessionId invalido",
		},
		{
			name:    "control byte del in sessionId",
			raw:     []byte("{\"v\":1,\"type\":\"ping\",\"from\":\"viewer\",\"sessionId\":\"sess\\u007fid\"}"),
			allowed: RemoteSessionTypes,
			wantErr: "sessionId invalido",
		},
		{
			name:    "internal whitespace in sessionId",
			raw:     []byte(`{"v":1,"type":"ping","from":"viewer","sessionId":"sess id"}`),
			allowed: RemoteSessionTypes,
			wantErr: "sessionId invalido",
		},
		{
			name:    "wildcard star in sessionId",
			raw:     []byte(`{"v":1,"type":"ping","from":"viewer","sessionId":"sess.*.id"}`),
			allowed: RemoteSessionTypes,
			wantErr: "sessionId invalido",
		},
		{
			name:    "wildcard gt in sessionId",
			raw:     []byte(`{"v":1,"type":"ping","from":"viewer","sessionId":"sess.>"}`),
			allowed: RemoteSessionTypes,
			wantErr: "sessionId invalido",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.raw, tc.allowed)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestDecodeNormalizesSessionIDAndVersion(t *testing.T) {
	raw := []byte(`{"v":99,"type":"pong","from":"agent","sessionId":"  sess-abc  "}`)
	env, err := Decode(raw, RemoteDebugTypes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.SessionID != "sess-abc" {
		t.Fatalf("SessionID = %q, want %q", env.SessionID, "sess-abc")
	}
	if env.Version != 1 {
		t.Fatalf("Version = %d, want 1", env.Version)
	}
}

func TestIsControlSubject(t *testing.T) {
	if !IsControlSubject("  tenant.1.site.2.agent.3.remote-debug.control  ", ".remote-debug.control") {
		t.Fatal("expected control subject match")
	}
	if IsControlSubject("tenant.1.site.2.agent.3.remote-debug.logs", ".remote-debug.control") {
		t.Fatal("did not expect non-control subject match")
	}
}
