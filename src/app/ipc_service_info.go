package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"discovery/app/core/buildinfo"
)

// GetIPCServiceInfo expõe ao console o par UI × serviço do último handshake
// (versão/protocolo) para avisar sobre par misto (A.2.2). Fora do modo
// companion (sem serviço/handshake) devolve connected=false.
func (a *App) GetIPCServiceInfo() (string, error) {
	uiVersion := strings.TrimSpace(buildinfo.Version)
	serviceVersion := ""
	protocol := 0
	connected := false
	if a != nil && a.ipcClient != nil {
		serviceVersion, protocol, connected = a.ipcClient.ServiceHandshake()
	}

	mismatch := connected && serviceVersion != "" && uiVersion != "" && serviceVersion != uiVersion

	// P1.4 — ESTADO DO CANAL DE GRAVAÇÃO na UI.
	//
	// O binding existia, mas só expunha versão/protocolo e NUNCA teve dados
	// (recordServiceHandshake só era chamado no handshake admin — ver §7.4(d) do
	// PLANO_CORRECAO_CANAL_ADMIN_IPC.md). A UI descobria o modo somente-leitura
	// apenas no handshake e só LOGAVA. Agora o painel de Diagnóstico/Sobre e o
	// banner têm de onde ler: capacidade + pipe + último erro do canal.
	adminOk := false
	adminPipe := IPCPipeAdminName
	if a != nil && a.ipcClient != nil {
		adminOk = a.ipcClient.AdminChannelAuthenticated()
	}
	// O circuit breaker é ESTADO GLOBAL do processo (não do client), então o
	// último erro é lido direto dele — assim o painel de diagnóstico continua
	// correto mesmo quando a App ainda não tem ipcClient atribuído.
	circuitTrips, circuitRejects, circuitOpen := IPCWriteCircuitStats()
	lastWriteError := IPCWriteBreakerLastError()

	body, err := json.Marshal(map[string]any{
		"uiVersion":      uiVersion,
		"serviceVersion": serviceVersion,
		"protocol":       protocol,
		"connected":      connected,
		"mismatch":       mismatch,
		// Estado do canal de gravação (UI/estado do canal — P1.4).
		"adminOk":       adminOk,
		"adminPipe":     adminPipe,
		"writesBlocked": circuitOpen,
		"circuitTrips":  circuitTrips,
		"circuitRejects": circuitRejects,
		"lastWriteError": lastWriteError,
	})
	if err != nil {
		return "", fmt.Errorf("nao foi possivel serializar o estado do servico: %w", err)
	}
	return string(body), nil
}
