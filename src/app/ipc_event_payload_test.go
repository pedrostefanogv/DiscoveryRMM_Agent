package app

import "testing"

// Testes da ponte de eventos serviço ↔ UI companion (named pipe).
//
// Regressão do bug "chat/servidor offline com o servidor no ar":
//   - o serviço empacotava EmitEvent(name, mapa) / EmitEvent(name, valor) sem
//     nenhum campo no payload IPC (o loop de pares era a única forma tratada);
//   - a UI reconstruía um slice plano e chamava EmitEvent(name, chave, valor...),
//     que no Wails v3 vira Event.Data = []any (array) — o frontend lia
//     data.connected como undefined → sempre offline.
// O contrato agora é: campos do evento no payload IPC e UM único argumento
// (objeto ou valor cru) ao emitir para o frontend.

func TestBuildIPCEventPayload_SingleMapKeepsFields(t *testing.T) {
	payload := buildIPCEventPayload("agent:connectivity", map[string]any{
		"connected":    true,
		"transport":    "nats-wss",
		"apiReachable": true,
	})

	if payload["name"] != "agent:connectivity" {
		t.Fatalf("name inesperado: %v", payload["name"])
	}
	if payload["connected"] != true {
		t.Fatalf("connected sumiu do payload IPC: %#v", payload)
	}
	if payload["transport"] != "nats-wss" {
		t.Fatalf("transport sumiu do payload IPC: %#v", payload)
	}
	if payload["apiReachable"] != true {
		t.Fatalf("apiReachable sumiu do payload IPC: %#v", payload)
	}
	if _, ok := payload[ipcEventRawKey]; ok {
		t.Fatalf("mapa não deve usar a chave reservada: %#v", payload)
	}
}

func TestBuildIPCEventPayload_SingleRawValue(t *testing.T) {
	const raw = "{\"kind\":\"question\"}"
	payload := buildIPCEventPayload("chat:question", raw)

	if payload["name"] != "chat:question" {
		t.Fatalf("name inesperado: %v", payload["name"])
	}
	got, ok := payload[ipcEventRawKey]
	if !ok {
		t.Fatalf("valor cru precisa ir sob %q: %#v", ipcEventRawKey, payload)
	}
	if got != raw {
		t.Fatalf("valor cru alterado: %v", got)
	}
	if len(payload) != 2 {
		t.Fatalf("payload deveria ter só name+valor cru: %#v", payload)
	}
}

func TestBuildIPCEventPayload_KeyValuePairs(t *testing.T) {
	payload := buildIPCEventPayload("agent:onboarding", "reason", "zero-touch-pending", "mode", "awaiting-approval")

	if payload["reason"] != "zero-touch-pending" || payload["mode"] != "awaiting-approval" {
		t.Fatalf("pares chave/valor perdidos: %#v", payload)
	}
}

func TestBuildIPCEventPayload_NoData(t *testing.T) {
	payload := buildIPCEventPayload("notification:new")
	if len(payload) != 1 || payload["name"] != "notification:new" {
		t.Fatalf("evento sem dados deveria carregar só o name: %#v", payload)
	}
}

func TestBuildIPCEventPayload_MapDoesNotOverrideName(t *testing.T) {
	payload := buildIPCEventPayload("agent:connectivity", map[string]any{
		"name":      "hijack",
		"connected": true,
	})
	if payload["name"] != "agent:connectivity" {
		t.Fatalf("mapa do evento não pode sobrescrever o name: %#v", payload)
	}
	if payload["connected"] != true {
		t.Fatalf("connected deveria permanecer: %#v", payload)
	}
}

func TestIPCEventToFrontend_MapBecomesSingleObjectArgument(t *testing.T) {
	name, data := ipcEventToFrontend(buildIPCEventPayload("agent:connectivity", map[string]any{
		"connected":    true,
		"transport":    "nats-wss",
		"apiReachable": true,
	}))

	if name != "agent:connectivity" {
		t.Fatalf("nome inesperado: %q", name)
	}
	// Um ÚNICO argumento: mais de um faria o Wails v3 entregar um array ao JS.
	if len(data) != 1 {
		t.Fatalf("esperava exatamente 1 argumento, veio %d (%#v)", len(data), data)
	}
	obj, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("argumento deveria ser um objeto (map), veio %T", data[0])
	}
	if obj["connected"] != true {
		t.Fatalf("frontend perderia connected: %#v", obj)
	}
	if _, ok := obj["name"]; ok {
		t.Fatalf("name não deve ir nos dados do evento: %#v", obj)
	}
}

func TestIPCEventToFrontend_RawValueRoundTrip(t *testing.T) {
	const raw = "{\"kind\":\"question\"}"
	name, data := ipcEventToFrontend(buildIPCEventPayload("chat:question", raw))

	if name != "chat:question" {
		t.Fatalf("nome inesperado: %q", name)
	}
	if len(data) != 1 || data[0] != raw {
		t.Fatalf("valor cru não sobreviveu ao round-trip: %#v", data)
	}
}

func TestIPCEventToFrontend_KeyValuePairsBecomeObject(t *testing.T) {
	_, data := ipcEventToFrontend(buildIPCEventPayload("agent:onboarding", "reason", "config-applied", "mode", "normal"))

	if len(data) != 1 {
		t.Fatalf("esperava 1 argumento, veio %d", len(data))
	}
	obj, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("esperava objeto, veio %T", data[0])
	}
	if obj["reason"] != "config-applied" || obj["mode"] != "normal" {
		t.Fatalf("campos perdidos: %#v", obj)
	}
}

func TestIPCEventToFrontend_EmptyName(t *testing.T) {
	name, data := ipcEventToFrontend(map[string]any{"connected": true})
	if name != "" || data != nil {
		t.Fatalf("payload sem name deveria ser ignorado (name=%q data=%#v)", name, data)
	}
}

func TestIPCEventToFrontend_SnapshotShape(t *testing.T) {
	// Payload exato do IPCMsgStatus (snapshot de conectividade do serviço).
	payload := map[string]any{
		"name":                 "agent:status_snapshot",
		"connected":            true,
		"transportConnected":   true,
		"apiReachable":         true,
		"transport":            "nats-wss",
		"reason":               "pong global recebido ha 57s",
		"lastEvent":            "conectado",
		"lastGlobalPongAtUtc":  "2026-10-07T23:04:49Z",
		"globalPongStale":      false,
		"onboardingMode":       "normal",
		"onboardingConfigured": true,
		"onboardingMessage":    nil,
	}
	_, data := ipcEventToFrontend(payload)
	obj, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("esperava objeto, veio %T", data[0])
	}
	if obj["connected"] != true {
		t.Fatalf("snapshot chegaria offline no frontend: %#v", obj)
	}
	if obj["transport"] != "nats-wss" {
		t.Fatalf("transport perdido: %#v", obj)
	}
	// O chat usa apiReachable para escolher o aviso especifico (API x transporte).
	if obj["apiReachable"] != true {
		t.Fatalf("apiReachable perdido: %#v", obj)
	}
}

// ── Revisão 2: normalização no ponto único de emissão e estado companion ────

func TestNormalizeEventArgs_PairsBecomeSingleMap(t *testing.T) {
	out := normalizeEventArgs([]any{"reason", "zero-touch-pending", "mode", "awaiting-approval"})
	if len(out) != 1 {
		t.Fatalf("pares deveriam virar 1 argumento, veio %d (%#v)", len(out), out)
	}
	m, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("esperava mapa, veio %T", out[0])
	}
	if m["reason"] != "zero-touch-pending" || m["mode"] != "awaiting-approval" {
		t.Fatalf("campos perdidos: %#v", m)
	}
}

func TestNormalizeEventArgs_PreservesOtherShapes(t *testing.T) {
	singleMap := []any{map[string]any{"connected": true}}
	if got := normalizeEventArgs(singleMap); len(got) != 1 {
		t.Fatalf("mapa único deveria passar intacto: %#v", got)
	}
	singleRaw := []any{"{\"a\":1}"}
	if got := normalizeEventArgs(singleRaw); len(got) != 1 || got[0] != singleRaw[0] {
		t.Fatalf("valor cru deveria passar intacto: %#v", got)
	}
	if got := normalizeEventArgs(nil); got != nil {
		t.Fatalf("sem dados deveria continuar nil: %#v", got)
	}
	if got := normalizeEventArgs([]any{}); len(got) != 0 {
		t.Fatalf("lista vazia deveria continuar vazia: %#v", got)
	}
	if got := normalizeEventArgs([]any{nil}); len(got) != 1 || got[0] != nil {
		t.Fatalf("nil único deveria passar intacto: %#v", got)
	}
	// 3 argumentos (ímpar) e chave não-string NÃO são o formato chave/valor.
	if got := normalizeEventArgs([]any{"a", 1, "b"}); len(got) != 3 {
		t.Fatalf("número ímpar de argumentos deveria passar intacto: %#v", got)
	}
	if got := normalizeEventArgs([]any{1, 2}); len(got) != 2 {
		t.Fatalf("chave não-string deveria passar intacto: %#v", got)
	}
}

func TestNormalizeEventArgs_ThenBuildPayloadRoundTrip(t *testing.T) {
	// EmitEvent(agent:onboarding, "reason", X, "mode", Y): a normalização
	// transforma em 1 mapa e o payload IPC mantém os campos.
	name, data := ipcEventToFrontend(buildIPCEventPayload("agent:onboarding", normalizeEventArgs([]any{"reason", "config-applied", "mode", "normal"})...))
	if name != "agent:onboarding" {
		t.Fatalf("nome inesperado: %q", name)
	}
	obj, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("esperava objeto, veio %T", data[0])
	}
	if obj["reason"] != "config-applied" || obj["mode"] != "normal" {
		t.Fatalf("campos perdidos no round-trip: %#v", obj)
	}
}

func TestBuildIPCEventPayload_MapStringString(t *testing.T) {
	payload := buildIPCEventPayload("store:catalog-updated", map[string]string{"reason": "rotation"})
	if payload["reason"] != "rotation" {
		t.Fatalf("map[string]string não virou campos do payload: %#v", payload)
	}
	if _, ok := payload[ipcEventRawKey]; ok {
		t.Fatalf("map[string]string não deveria usar a chave reservada: %#v", payload)
	}
}

func TestBuildIPCEventPayload_NotificationKeepsFields(t *testing.T) {
	// notification:new era enviada pelo serviço como mapa e chegava vazia à UI.
	payload := buildIPCEventPayload("notification:new", map[string]any{
		"id": "toast-1", "mode": "notify_only", "severity": "high", "title": "Instalacao",
	})
	for _, k := range []string{"id", "mode", "severity", "title"} {
		if _, ok := payload[k]; !ok {
			t.Fatalf("campo %q perdido no payload IPC: %#v", k, payload)
		}
	}
}

func TestStoreCompanionStatus_ConnectivityEventDoesNotWipePong(t *testing.T) {
	a := &App{}
	// 1º snapshot completo (o que a UI recebe a cada ~5s).
	a.storeCompanionStatus(map[string]any{
		"connected":           true,
		"transportConnected":  true,
		"transport":           "nats-wss",
		"reason":              "pong global recebido ha 57s",
		"lastGlobalPongAtUtc": "2026-10-07T23:04:49Z",
		"globalPongStale":     false,
	})
	// Transição agent:connectivity: só connected/transport/reason.
	a.storeCompanionStatus(map[string]any{
		"connected": true,
		"transport": "nats-wss",
		"reason":    "transport=nats-wss pongAge=never",
	})

	st := a.companionStatus
	if st == nil {
		t.Fatal("estado companion não foi criado")
	}
	if st.LastGlobalPongAtUTC != "2026-10-07T23:04:49Z" {
		t.Fatalf("evento de conectividade zerou o último pong: %q", st.LastGlobalPongAtUTC)
	}
	if !st.TransportConnected {
		t.Fatal("sem transportConnected no evento, deve herdar o efetivo (true)")
	}
	if st.LastEvent == "" {
		t.Fatal("reason deveria alimentar o LastEvent")
	}
}

func TestStoreCompanionStatus_APIDownKeepsTransportUp(t *testing.T) {
	a := &App{}
	a.storeCompanionStatus(map[string]any{
		"connected":          false,
		"transportConnected": true,
		"transport":          "nats-wss",
		"reason":             "API inacessivel",
	})
	st := a.companionStatus
	if st.Connected {
		t.Fatal("API fora deveria marcar connected=false")
	}
	if !st.TransportConnected {
		t.Fatal("NATS de pé deveria manter transportConnected=true (diagnóstico)")
	}
	if st.LastEvent != "API inacessivel" {
		t.Fatalf("motivo deveria ser preservado, veio %q", st.LastEvent)
	}
}
