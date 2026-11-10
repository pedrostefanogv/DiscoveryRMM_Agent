package app

// Testes da visibilidade do canal de gravação (P1.4/P1.7/P1.8 do
// PLANO_CORRECAO_CANAL_ADMIN_IPC.md): o estado do canal tem de chegar à UI e ao
// heartbeat. Sem isso, "somente-leitura" é silencioso — foi o que deixou o
// incidente de 2026-10-17 passar despercebido (leituras ok, mutações todas
// falhando).

import (
	"encoding/json"
	"net"
	"strings"
	"testing"

	"discovery/app/core/agentconn"
)

// O servidor reporta a capacidade pelo estado REAL do listener admin: nil
// (não subiu) → false; listener presente → true.
func TestAdminChannelActive_SemListener(t *testing.T) {
	if (*IPCServer)(nil).AdminChannelActive() {
		t.Fatal("servidor nil não pode reportar canal admin ativo")
	}
	s := &IPCServer{conns: make(map[net.Conn]*ipcClientConn)}
	if s.AdminChannelActive() {
		t.Fatal("servidor sem adminListener deve reportar canal de gravação INATIVO")
	}
}

// O binding da UI precisa carregar adminOk/adminPipe/writesBlocked — era o
// painel de Diagnóstico que nunca teve dados (§7.4(d) do plano).
func TestGetIPCServiceInfo_ExpoeEstadoDoCanalDeGravacao(t *testing.T) {
	resetIPCWriteBreaker()
	defer resetIPCWriteBreaker()

	raw, err := (&App{}).GetIPCServiceInfo()
	if err != nil {
		t.Fatalf("GetIPCServiceInfo: %v", err)
	}
	var doc map[string]any
	if uErr := json.Unmarshal([]byte(raw), &doc); uErr != nil {
		t.Fatalf("JSON inválido: %v (%q)", uErr, raw)
	}
	for _, key := range []string{"adminOk", "adminPipe", "writesBlocked", "circuitTrips", "circuitRejects", "lastWriteError"} {
		if _, ok := doc[key]; !ok {
			t.Fatalf("campo %q ausente em GetIPCServiceInfo: %q", key, raw)
		}
	}
	if doc["adminPipe"] != IPCPipeAdminName {
		t.Fatalf("adminPipe = %v, want %q", doc["adminPipe"], IPCPipeAdminName)
	}
	if doc["adminOk"] != false {
		t.Fatalf("sem canal admin autenticado, adminOk deveria ser false; got %v", doc["adminOk"])
	}
}

// Com o breaker aberto o binding marca writesBlocked=true e traz o último erro
// para o painel de diagnóstico.
func TestGetIPCServiceInfo_MarcaGravacaoBloqueada(t *testing.T) {
	resetIPCWriteBreaker()
	defer resetIPCWriteBreaker()

	OpenIPCWriteCircuit("support:agent_ticket_create",
		newIPCWriteUnavailable("support:agent_ticket_create", "pipe não existe"))
	raw, err := (&App{}).GetIPCServiceInfo()
	if err != nil {
		t.Fatalf("GetIPCServiceInfo: %v", err)
	}
	var doc map[string]any
	if uErr := json.Unmarshal([]byte(raw), &doc); uErr != nil {
		t.Fatalf("JSON inválido: %v", uErr)
	}
	if doc["writesBlocked"] != true {
		t.Fatalf("writesBlocked deveria ser true com o breaker aberto; got %v", doc["writesBlocked"])
	}
	lastErr, _ := doc["lastWriteError"].(string)
	if !strings.Contains(lastErr, IPCPipeAdminName) {
		t.Fatalf("lastWriteError deveria citar o pipe; got %q", lastErr)
	}
}

// O texto do banner NÃO pode culpar o servidor de suporte — é exatamente o
// diagnóstico errado que o incidente produziu.
func TestIPCWriteChannelMessage_NaoCulpaServidor(t *testing.T) {
	msg := ipcWriteChannelMessage(false, IPCPipeAdminName)
	if msg == "" {
		t.Fatal("canal inativo exige mensagem para o usuário")
	}
	if !strings.Contains(msg, IPCPipeAdminName) {
		t.Fatalf("mensagem deveria citar o pipe; got %q", msg)
	}
	if !strings.Contains(msg, "Reinicie o serviço") {
		t.Fatalf("mensagem deveria orientar reiniciar o serviço; got %q", msg)
	}
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "não no servidor") {
		t.Fatalf("mensagem deveria desmentir o diagnóstico 'servidor fora do ar'; got %q", msg)
	}
	if strings.Contains(lower, "servidor de suporte fora") || strings.Contains(lower, "instável") {
		t.Fatalf("mensagem não pode afirmar indisponibilidade do servidor; got %q", msg)
	}
	if got := ipcWriteChannelMessage(true, IPCPipeAdminName); got != "" {
		t.Fatalf("canal ativo não precisa de banner; got %q", got)
	}
}

// A capacidade guardada na UI companion tem de refletir o snapshot e ser
// legível pelo frontend com o MESMO vocabulário do binding.
func TestStoreCompanionIPCCapability(t *testing.T) {
	a := &App{}
	if a.getCompanionIPCCapability() != nil {
		t.Fatal("sem snapshot a capacidade deveria ser nil (serviço antigo)")
	}
	a.storeCompanionIPCCapability(false, "")
	cap := a.getCompanionIPCCapability()
	if cap == nil {
		t.Fatal("capacidade deveria ter sido guardada")
	}
	if cap["adminOk"] != false || cap["writesBlocked"] != true {
		t.Fatalf("estado inválido: %+v", cap)
	}
	if cap["adminPipe"] != IPCPipeAdminName {
		t.Fatalf("pipe vazio deveria cair no pipe admin padrão; got %v", cap["adminPipe"])
	}
	a.storeCompanionIPCCapability(true, IPCPipeAdminName)
	cap = a.getCompanionIPCCapability()
	if cap["adminOk"] != true || cap["writesBlocked"] != false {
		t.Fatalf("após recuperação o estado deveria virar ok: %+v", cap)
	}
}

// O heartbeat precisa CARREGAR a capacidade (P1.7): é o que permite ao portal
// alertar "agente em somente-leitura" sem esperar o usuário reclamar.
func TestHeartbeatCarregaCapacidadeDoCanal(t *testing.T) {
	adminOk := false
	metrics := agentconn.AgentHeartbeatMetrics{
		IpcAdminOk:   &adminOk,
		IpcAdminPipe: IPCPipeAdminName,
	}
	hb := agentconn.BuildHeartbeatWithMetrics(agentconn.Config{}, "10.0.0.1", metrics)
	if hb.IpcAdminOk == nil || *hb.IpcAdminOk {
		t.Fatalf("payload do heartbeat sem ipcAdminOk=false: %+v", hb.IpcAdminOk)
	}
	if hb.IpcAdminPipe != IPCPipeAdminName {
		t.Fatalf("payload do heartbeat sem o pipe admin: %q", hb.IpcAdminPipe)
	}
	raw, err := json.Marshal(hb)
	if err != nil {
		t.Fatalf("heartbeat não serializa: %v", err)
	}
	if !strings.Contains(string(raw), `"ipcAdminOk":false`) {
		t.Fatalf("heartbeat JSON deveria conter ipcAdminOk=false: %s", raw)
	}
	if !strings.Contains(string(raw), `"ipcAdminPipe":"`+strings.ReplaceAll(IPCPipeAdminName, "\\", "\\\\")+`"`) {
		t.Fatalf("heartbeat JSON deveria conter o nome do pipe admin: %s", raw)
	}
}
