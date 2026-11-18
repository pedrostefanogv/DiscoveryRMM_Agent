package remotesession

import (
	"fmt"
	"strings"
	"time"
)

// Issue codes para preflight RDP (estruturados para consumo na UI/API/CLI).
const (
	IssueTermServiceNotRunning = "term_service_not_running"
	IssueTermServiceDisabled   = "term_service_disabled"
	IssueRDPDisabledRegistry   = "rdp_disabled_registry"
	IssueRDPPortNotListening   = "rdp_port_not_listening"
	IssueRDPUnsupportedOS      = "rdp_unsupported_os"
)

// RDPStatus descreve o resultado estritamente somente-leitura do preflight RDP no host.
type RDPStatus struct {
	Ready              bool     `json:"ready"`
	TermServiceState   string   `json:"termServiceState"`
	TermServiceStart   string   `json:"termServiceStart,omitempty"`
	FDenyTSConnections *uint32  `json:"fDenyTSConnections,omitempty"`
	RDPEnabled         bool     `json:"rdpEnabled"`
	Port               int      `json:"port"`
	Listening          bool     `json:"listening"`
	Issues             []string `json:"issues,omitempty"`
	Hints              []string `json:"hints,omitempty"`
	CheckedAtUtc       string   `json:"checkedAtUtc"`
}

func normalizeRDPPort(port int) int {
	if port <= 0 || port > 65535 {
		return 3389
	}
	return port
}

func evaluateRDPStatus(termState, termStart string, fDeny *uint32, port int, listening bool, now time.Time) RDPStatus {
	effPort := normalizeRDPPort(port)
	state := strings.ToLower(strings.TrimSpace(termState))
	if state == "" {
		state = "unknown"
	}
	start := strings.ToLower(strings.TrimSpace(termStart))

	rdpEnabled := fDeny != nil && *fDeny == 0
	var issues []string
	var hints []string

	if start == "disabled" {
		issues = append(issues, IssueTermServiceDisabled)
		hints = append(hints, "Altere o tipo de inicializacao do servico TermService para Manual ou Automatico.")
	}
	if state != "running" {
		issues = append(issues, IssueTermServiceNotRunning)
		hints = append(hints, "Inicie o servico TermService (Servicos de Area de Trabalho Remota).")
	}
	if !rdpEnabled {
		issues = append(issues, IssueRDPDisabledRegistry)
		hints = append(hints, "Habilite Area de Trabalho Remota em Configuracoes > Sistema > Area de Trabalho Remota (fDenyTSConnections=0).")
	}
	if !listening {
		issues = append(issues, IssueRDPPortNotListening)
		hints = append(hints, fmt.Sprintf("Verifique se o listener RDP esta ativo e sem bloqueio em 127.0.0.1:%d.", effPort))
	}

	if now.IsZero() {
		now = time.Now().UTC()
	}

	return RDPStatus{
		Ready:              state == "running" && rdpEnabled && listening,
		TermServiceState:   state,
		TermServiceStart:   start,
		FDenyTSConnections: fDeny,
		RDPEnabled:         rdpEnabled,
		Port:               effPort,
		Listening:          listening,
		Issues:             issues,
		Hints:              hints,
		CheckedAtUtc:       now.UTC().Format(time.RFC3339),
	}
}
