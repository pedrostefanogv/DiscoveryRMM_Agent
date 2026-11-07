//go:build windows

package native

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// F11 — Inventário de máquinas virtuais do Hyper-V.
//
// Coletor CONDICIONAL: só executa quando o host realmente tem o role Hyper-V
// (a role define o serviço vmms). Sem Hyper-V, devolve vazio com motivo, sem
// chamar PowerShell.
//
// Fonte: CIM Msvm_* no namespace root\virtualization\v2 — via Get-VM, que é
// a forma suportada e estável de acessar esse namespace. Consultamos apenas
// Estado, Nome, Memória e Uptime (read-only).
//
// NÃO coletamos: configuração de rede/vSwitch, discos virtuais anexados nem
// checkpoints. Esses exigem consultas adicionais pesadas e não fazem parte do
// escopo desta task; ficam registrados como evolução.
// ─────────────────────────────────────────────────────────────────────────────

// hyperVTimeout limita a coleta. Get-VM em host com muitas VMs pode demorar;
// 20s é folgado para um inventário e curto o bastante para não travar o ciclo.
const hyperVTimeout = 20 * time.Second

// maxHyperVVMs limita quantas VMs entram no payload. Um host com centenas de
// VMs geraria payload grande; o teto protege a serialização.
const maxHyperVVMs = 500

// HyperVInventory é o resultado do F11.
type HyperVInventory struct {
	// Enabled indica que a role Hyper-V está presente na máquina.
	Enabled bool `json:"enabled"`
	// VMs é a lista de máquinas virtuais encontradas.
	VMs []HyperVVM `json:"vms,omitempty"`
	// HostMemoryTotalMB é a memória física total do host, para contexto.
	HostMemoryTotalMB int `json:"hostMemoryTotalMb,omitempty"`
	// Reason explica resultado vazio.
	Reason string `json:"reason,omitempty"`
}

// HyperVVM descreve uma VM. Campos derivados do Msvm_ComputerSystem /
// Msvm_SummaryInformation.
type HyperVVM struct {
	Name string `json:"name"`
	// State é o estado legível ("Running", "Off", "Saved", "Paused"...).
	State string `json:"state"`
	// StateCode é o valor bruto do CIM, útil para o servidor decidir sem
	// depender da string localizada.
	StateCode int `json:"stateCode,omitempty"`
	// MemoryAssignedMB é a RAM atribuída à VM.
	MemoryAssignedMB int `json:"memoryAssignedMb,omitempty"`
	// UptimeSeconds é o tempo ligado (0 quando desligada).
	UptimeSeconds int64 `json:"uptimeSeconds,omitempty"`
	// Generation é a geração da VM (1 ou 2).
	Generation int `json:"generation,omitempty"`
	// IntegrationServicesVersion é a versão reportada pelo guest, quando há.
	IntegrationServicesVersion string `json:"integrationServicesVersion,omitempty"`
}

// hyperVStateNames mapeia o código de estado do CIM para nome estável.
// O CIM usa inteiros; a string que o Windows devolve é LOCALIZADA, então
// mapeamos nós mesmos para o payload não depender do idioma do host.
// Fonte: enum Msvm_ComputerSystem.EnabledState do Hyper-V.
var hyperVStateNames = map[int]string{
	0:     "Unknown",
	2:     "Running",
	3:     "Off",
	4:     "ShuttingDown",
	6:     "Saved",
	9:     "Paused",
	10:    "Starting",
	11:    "Snapshotting",
	32768: "Pausing",
	32769: "Resuming",
	32770: "FastSaved",
	32771: "FastSaving",
	32773: "ForceShutdown",
	32774: "ForceReboot",
	32776: "Hibernated",
}

// hyperVStateName converte o código de estado em nome estável. É PURA.
// Código desconhecido devolve "Unknown:<n>" em vez de vazio, para o operador
// ver o valor real e nós não inventarmos um significado.
func hyperVStateName(code int) string {
	if name, ok := hyperVStateNames[code]; ok {
		return name
	}
	return "Unknown:" + strconv.Itoa(code)
}

// hyperVAvailable informa se a role Hyper-V está presente. Checa o serviço
// vmms (Virtual Machine Management), que só existe com a role instalada.
// É a checagem mais barata e não exige elevação.
func hyperVAvailable() bool {
	// O serviço vmms é instalado pela role. Consultamos via SCM, sem WMI.
	return serviceExists("vmms")
}

// hyperVListScript lista as VMs em JSON. Usa Get-VM (CIM Msvm_* por baixo).
// -ErrorAction SilentlyContinue garante que ausência de Hyper-V não vire erro.
const hyperVListScript = `$ErrorActionPreference = 'SilentlyContinue'
$vms = @(Get-VM | ForEach-Object {
    [PSCustomObject]@{
        Name             = $_.Name
        StateCode        = [int]$_.State
        StateName        = [string]$_.State
        MemoryAssignedMB = [int]($_.MemoryAssigned / 1MB)
        UptimeSeconds    = [int64]$_.Uptime.TotalSeconds
        Generation       = [int]$_.Generation
        IntegrationVersion = [string]$_.IntegrationServicesVersion
    }
})
if ($vms.Count -eq 0) { '[]' } else { $vms | ConvertTo-Json -Depth 3 -Compress }`

// CollectHyperVInventario coleta o inventário de VMs do Hyper-V.
//
// Contrato: nunca erro, nunca panic. Sem Hyper-V devolve Enabled=false com
// motivo. Falha de execução/parse degrada para lista vazia com motivo.
func CollectHyperVInventario(ctx context.Context) HyperVInventory {
	if runtime.GOOS != "windows" {
		return HyperVInventory{Reason: "nao-windows"}
	}
	if !hyperVAvailable() {
		return HyperVInventory{Reason: "hyper-v-ausente"}
	}

	inv := HyperVInventory{Enabled: true}

	vmCtx, cancel := context.WithTimeout(ctx, hyperVTimeout)
	defer cancel()

	cmd := exec.CommandContext(vmCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", hyperVListScript)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if vmCtx.Err() == context.DeadlineExceeded {
			inv.Reason = "timeout"
		} else {
			inv.Reason = "falha-ao-executar-powershell"
		}
		return inv
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" {
		inv.Reason = "saida-vazia"
		return inv
	}
	if trimmed == "[]" {
		inv.Reason = "sem-vms"
		return inv
	}

	type rawVM struct {
		Name               string `json:"Name"`
		StateCode          any    `json:"StateCode"`
		StateName          any    `json:"StateName"`
		MemoryAssignedMB   any    `json:"MemoryAssignedMB"`
		UptimeSeconds      any    `json:"UptimeSeconds"`
		Generation         any    `json:"Generation"`
		IntegrationVersion any    `json:"IntegrationVersion"`
	}

	// ConvertTo-Json devolve OBJETO quando há uma única VM e ARRAY quando há
	// várias. Aceitamos os dois — o mesmo cuidado do coletor SMART.
	var raw []rawVM
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		var single rawVM
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			inv.Reason = "falha-ao-parsear"
			return inv
		}
		raw = []rawVM{single}
	}

	for _, r := range raw {
		if len(inv.VMs) >= maxHyperVVMs {
			break
		}
		name := strings.TrimSpace(wmiString(map[string]any{"v": r.Name}, "v"))
		if name == "" {
			continue
		}
		code := wmiInt(map[string]any{"v": r.StateCode}, "v")
		inv.VMs = append(inv.VMs, HyperVVM{
			Name:                       name,
			State:                      hyperVStateName(code),
			StateCode:                  code,
			MemoryAssignedMB:           wmiInt(map[string]any{"v": r.MemoryAssignedMB}, "v"),
			UptimeSeconds:              int64(wmiInt(map[string]any{"v": r.UptimeSeconds}, "v")),
			Generation:                 wmiInt(map[string]any{"v": r.Generation}, "v"),
			IntegrationServicesVersion: strings.TrimSpace(wmiString(map[string]any{"v": r.IntegrationVersion}, "v")),
		})
	}

	if len(inv.VMs) == 0 {
		inv.Reason = "sem-vms"
	}
	return inv
}
