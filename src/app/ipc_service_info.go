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

	body, err := json.Marshal(map[string]any{
		"uiVersion":      uiVersion,
		"serviceVersion": serviceVersion,
		"protocol":       protocol,
		"connected":      connected,
		"mismatch":       mismatch,
	})
	if err != nil {
		return "", fmt.Errorf("nao foi possivel serializar o estado do servico: %w", err)
	}
	return string(body), nil
}
