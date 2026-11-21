package remotesession

import "testing"

// O bloco "webrtc" do payload de start precisa ser lido de forma TOLERANTE mas
// nunca "otimista": qualquer dúvida resulta em WebRTC desligado (caminho NATS),
// porque o agente é o ponto de coerção e o viewer é não confiável.

func TestParseWebRtcPolicy_BlocoAusenteDesliga(t *testing.T) {
	for name, raw := range map[string]any{
		"nil":        nil,
		"mapa vazio": map[string]any{},
		"string":     "habilitado",
		"numero":     float64(1),
		"array":      []any{"screen"},
	} {
		policy := ParseWebRtcPolicy(raw)
		if policy.Enabled {
			t.Fatalf("%s: WebRTC não pode ligar sem enabled=true explícito", name)
		}
		if !policy.FallbackToNats {
			t.Fatalf("%s: fallback deve continuar ligado", name)
		}
	}
}

func TestParseWebRtcPolicy_EnabledNaoBooleanoNaoLiga(t *testing.T) {
	// "true" como STRING é o erro clássico de serialização: não pode ligar.
	policy := ParseWebRtcPolicy(map[string]any{"enabled": "true"})
	if policy.Enabled {
		t.Fatal("enabled como string não pode ligar o WebRTC")
	}
}

func TestParseWebRtcPolicy_BlocoCompleto(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled": true,
		"kinds":   []any{"screen", "rdp"},
		"iceServers": []any{
			map[string]any{"urls": []any{"stun:stun.l.google.com:19302"}},
			map[string]any{
				"urls":       []any{"turn:turn.exemplo.com:3478"},
				"username":   "usuario",
				"credential": "segredo",
			},
		},
		"relayOnly":                  false,
		"fallbackToNats":             true,
		"disconnectedTimeoutSeconds": float64(7),
		"failedTimeoutSeconds":       float64(20),
		"keepAliveIntervalSeconds":   float64(3),
	})

	if !policy.Enabled {
		t.Fatal("esperava enabled=true")
	}
	if len(policy.Kinds) != 2 {
		t.Fatalf("esperava 2 kinds, obteve %d", len(policy.Kinds))
	}
	if len(policy.IceServers) != 2 {
		t.Fatalf("esperava 2 servidores ICE, obteve %d", len(policy.IceServers))
	}
	if policy.IceServers[1].Username != "usuario" || policy.IceServers[1].Credential != "segredo" {
		t.Fatal("credenciais do TURN não foram preservadas")
	}
	if policy.DisconnectedTimeoutSeconds != 7 || policy.FailedTimeoutSeconds != 20 || policy.KeepAliveIntervalSeconds != 3 {
		t.Fatalf("timeouts não aplicados: %+v", policy)
	}
	if !policy.HasStun() || !policy.HasTurn() {
		t.Fatal("esperava STUN e TURN detectados")
	}
}

func TestParseWebRtcPolicy_UrlsAceitaStringEArray(t *testing.T) {
	// Tolerância deliberada: o navegador aceita as duas formas e o servidor pode
	// emitir qualquer uma. Recusar string quebraria a negociação em silêncio.
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled": true,
		"iceServers": []any{
			map[string]any{"urls": "stun:stun.l.google.com:19302"},
			map[string]any{"urls": []any{"stun:stun.cloudflare.com:3478"}},
		},
	})

	if len(policy.IceServers) != 2 {
		t.Fatalf("esperava 2 servidores, obteve %d", len(policy.IceServers))
	}
	if policy.IceServers[0].Urls[0] != "stun:stun.l.google.com:19302" {
		t.Fatalf("url string não convertida: %+v", policy.IceServers[0])
	}
}

func TestParseWebRtcPolicy_DescartaUrlSemEsquema(t *testing.T) {
	// "stun.l.google.com:19302" sem esquema é o erro mais comum de digitação e
	// passaria silenciosamente para o stack de ICE, onde falharia sem explicação.
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled": true,
		"iceServers": []any{
			map[string]any{"urls": []any{"stun.l.google.com:19302", "stun:valido.example:3478"}},
		},
	})

	if len(policy.IceServers) != 1 {
		t.Fatalf("esperava 1 servidor, obteve %d", len(policy.IceServers))
	}
	if len(policy.IceServers[0].Urls) != 1 || policy.IceServers[0].Urls[0] != "stun:valido.example:3478" {
		t.Fatalf("URL inválida não foi descartada: %+v", policy.IceServers[0].Urls)
	}
}

func TestParseWebRtcPolicy_DeduplicaUrls(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"iceServers": []any{map[string]any{"urls": []any{"stun:a:3478", "stun:a:3478"}}},
	})

	if len(policy.IceServers[0].Urls) != 1 {
		t.Fatalf("esperava deduplicação, obteve %+v", policy.IceServers[0].Urls)
	}
}

func TestParseWebRtcPolicy_DescartaServidorSemUrl(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled": true,
		"iceServers": []any{
			map[string]any{"username": "sem-url"},
			map[string]any{"urls": []any{"stun:ok:3478"}},
		},
	})

	if len(policy.IceServers) != 1 {
		t.Fatalf("servidor sem url deveria ser descartado, obteve %d", len(policy.IceServers))
	}
}

func TestParseWebRtcPolicy_TimeoutsForaDeFaixaVoltamAoDefault(t *testing.T) {
	// Os defaults precisam ser os AFINADOS (5/15/2), não os do pion (25 s de
	// failed), senão o operador espera 25 s antes do fallback entrar.
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":                    true,
		"failedTimeoutSeconds":       float64(9999),
		"disconnectedTimeoutSeconds": float64(0),
		"keepAliveIntervalSeconds":   float64(-1),
		"iceServers":                 []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	if policy.FailedTimeoutSeconds != defaultWebRtcFailedTimeoutSeconds {
		t.Fatalf("esperava default %d, obteve %d", defaultWebRtcFailedTimeoutSeconds, policy.FailedTimeoutSeconds)
	}
	if policy.DisconnectedTimeoutSeconds != defaultWebRtcDisconnectedTimeoutSeconds {
		t.Fatalf("esperava default %d, obteve %d", defaultWebRtcDisconnectedTimeoutSeconds, policy.DisconnectedTimeoutSeconds)
	}
	if policy.KeepAliveIntervalSeconds != defaultWebRtcKeepAliveIntervalSeconds {
		t.Fatalf("esperava default %d, obteve %d", defaultWebRtcKeepAliveIntervalSeconds, policy.KeepAliveIntervalSeconds)
	}
}

func TestParseWebRtcPolicy_FallbackExplicitoFalse(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":        true,
		"fallbackToNats": false,
		"iceServers":     []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	if policy.FallbackToNats {
		t.Fatal("fallbackToNats=false deveria ser respeitado")
	}
}

func TestParseWebRtcPolicy_FallbackTipoErradoMantemTrue(t *testing.T) {
	// Tipo errado NUNCA desliga o fallback: o default seguro é ter saída.
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":        true,
		"fallbackToNats": "talvez",
		"iceServers":     []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	if !policy.FallbackToNats {
		t.Fatal("tipo errado em fallbackToNats deve manter o default seguro (true)")
	}
}

// ── Validate ────────────────────────────────────────────────────────────

func TestValidate_RelayOnlySemTurnRecusa(t *testing.T) {
	// A armadilha central: sem TURN não há candidato relay e a sessão NUNCA
	// conecta, deixando o operador em "conectando..." sem causa.
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"relayOnly":  true,
		"iceServers": []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	if reason := policy.Validate(); reason == "" {
		t.Fatal("relayOnly sem TURN deveria ser recusado")
	}
}

func TestValidate_RelayOnlyComTurnAceita(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"relayOnly":  true,
		"iceServers": []any{map[string]any{"urls": []any{"turn:t:3478"}}},
	})

	if reason := policy.Validate(); reason != "" {
		t.Fatalf("relayOnly com TURN deveria ser aceito, recusou por: %s", reason)
	}
}

func TestValidate_SemServidorIceRecusa(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{"enabled": true})
	if reason := policy.Validate(); reason == "" {
		t.Fatal("bloco sem servidor ICE utilizável deveria ser recusado")
	}
}

func TestValidate_DesligadoNuncaRecusa(t *testing.T) {
	// WebRTC desligado não pode impedir a sessão: ela segue pelo NATS.
	policy := disabledWebRtcPolicy()
	if reason := policy.Validate(); reason != "" {
		t.Fatalf("política desligada não deveria recusar, recusou por: %s", reason)
	}
}

// ── AllowsKind ──────────────────────────────────────────────────────────

func TestAllowsKind_RespeitaALista(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"kinds":      []any{"screen", "rdp"},
		"iceServers": []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	if !policy.AllowsKind("screen") {
		t.Fatal("screen está na lista e deveria ser autorizado")
	}
	if !policy.AllowsKind("RDP") {
		t.Fatal("kind deve ser comparado sem diferenciar maiúsculas")
	}
	if policy.AllowsKind("terminal") {
		t.Fatal("terminal não está na lista e NÃO pode ser autorizado")
	}
	if policy.AllowsKind("files") {
		t.Fatal("files não está na lista e NÃO pode ser autorizado")
	}
}

func TestAllowsKind_ListaVaziaNaoAutorizaNada(t *testing.T) {
	// Lista vazia é "nenhum", nunca "todos": um default implícito de "todos"
	// ligaria WebRTC em terminal, que é justamente o caso sem ganho.
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"iceServers": []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	if policy.AllowsKind("screen") {
		t.Fatal("lista de kinds vazia não pode autorizar screen")
	}
}

func TestAllowsKind_DesligadoNuncaAutoriza(t *testing.T) {
	policy := ParseWebRtcPolicy(map[string]any{
		"enabled":    false,
		"kinds":      []any{"screen"},
		"iceServers": []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})

	if policy.AllowsKind("screen") {
		t.Fatal("WebRTC desligado não pode autorizar nenhum kind")
	}
}

// ── HasStun/HasTurn ─────────────────────────────────────────────────────

func TestHasStunHasTurn_DistinguemOsEsquemas(t *testing.T) {
	stunOnly := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"iceServers": []any{map[string]any{"urls": []any{"stun:a:3478"}}},
	})
	if !stunOnly.HasStun() || stunOnly.HasTurn() {
		t.Fatal("servidor apenas STUN mal classificado")
	}

	turnOnly := ParseWebRtcPolicy(map[string]any{
		"enabled":    true,
		"iceServers": []any{map[string]any{"urls": []any{"turns:a:5349"}}},
	})
	if turnOnly.HasStun() || !turnOnly.HasTurn() {
		t.Fatal("servidor apenas TURN (turns:) mal classificado")
	}
}
