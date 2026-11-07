//go:build windows

package native

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// F14 — Storage Spaces (pools) e RDS Session Host (sessões).
//
// Duas coletas independentes no mesmo arquivo porque compartilham a mesma
// natureza: ambas são CONDICIONAIS a uma role/serviço e ambas são pequenas.
//
// SOMENTE-LEITURA: Get-StoragePool e qwinsta apenas consultam. Nenhum comando
// de criação/remoção de pool, nem logoff/encerramento de sessão de usuário —
// derrubar a sessão de um usuário é ação destrutiva e fora do escopo.
//
// F8 (abrir o Security log) e F13 (GPO/RSOP) NÃO são implementados aqui, por
// decisão de escopo do plano:
//   F8  — ler o Security log exige privilégio SeSecurityPrivilege e uma
//         política de RBAC no servidor que ainda não existe. Abrir o canal
//         sem esse controle exporia eventos de auditoria a qualquer operador.
//   F13 — GPO/RSOP tem custo estrutural alto (parsear RSOP de verdade) e a
//         modelagem de OU/domínio ainda não existe no produto. P3.
// Nenhum código de leitura de Security log ou de RSOP foi escrito.
// ─────────────────────────────────────────────────────────────────────────────

// storageRDSTimeout limita cada uma das duas coletas deste arquivo.
const storageRDSTimeout = 20 * time.Second

// maxStoragePools e maxRDSSessions limitam o payload.
const (
	maxStoragePools = 100
	maxRDSSessions  = 500
)

// StorageRDSInventory agrega as duas seções do F14.
type StorageRDSInventory struct {
	// StoragePools lista os pools de Storage Spaces, quando existirem.
	StoragePools []StoragePool `json:"storagePools,omitempty"`
	// RDSHost indica que a máquina é um Session Host (Remote Desktop).
	RDSHost bool `json:"rdsHost"`
	// RDSSessions lista as sessões abertas, quando for Session Host.
	RDSSessions []RDSSession `json:"rdsSessions,omitempty"`
	// Reason explica resultado vazio.
	Reason string `json:"reason,omitempty"`
}

// StoragePool é um pool de armazenamento (Storage Spaces).
type StoragePool struct {
	Name string `json:"name"`
	// HealthStatus é o estado reportado pelo próprio subsistema.
	HealthStatus string `json:"healthStatus,omitempty"`
	// OperationalStatus complementa HealthStatus (ex.: "OK", "Degraded").
	OperationalStatus string `json:"operationalStatus,omitempty"`
	// SizeGB / AllocatedGB dão a dimensão do pool.
	SizeGB      int64 `json:"sizeGb,omitempty"`
	AllocatedGB int64 `json:"allocatedGb,omitempty"`
	// DiskCount é quantos discos físicos compõem o pool.
	DiskCount int `json:"diskCount,omitempty"`
	// IsPrimordial marca o pool "Primordial", que é o agregado de discos crus
	// e NÃO um pool criado pelo usuário — o servidor deve ignorá-lo.
	IsPrimordial bool `json:"isPrimordial,omitempty"`
}

// RDSSession é uma sessão de Remote Desktop aberta no host.
type RDSSession struct {
	// SessionID é o identificador do Windows para a sessão.
	SessionID int `json:"sessionId"`
	// Username é o usuário logado (vazio em sessão de serviço).
	Username string `json:"username,omitempty"`
	// State é "Active", "Disc" (desconectada) ou "Listen".
	State string `json:"state,omitempty"`
	// SessionType distingue console de RDP.
	SessionType string `json:"sessionType,omitempty"`
}

// storagePoolScript lê os pools de Storage Spaces em JSON. Get-StoragePool é
// o cmdlet de consulta do subsistema; não altera nada.
const storagePoolScript = `$ErrorActionPreference = 'SilentlyContinue'
$pools = @(Get-StoragePool | ForEach-Object {
    $p = $_
    [PSCustomObject]@{
        Name              = $p.FriendlyName
        HealthStatus      = [string]$p.HealthStatus
        OperationalStatus = [string]($p.OperationalStatus -join ',')
        SizeGB            = [int64]($p.Size / 1GB)
        AllocatedGB       = [int64]($p.AllocatedSize / 1GB)
        IsPrimordial      = [bool]$p.IsPrimordial
        DiskCount         = [int]@($p | Get-PhysicalDisk -ErrorAction SilentlyContinue).Count
    }
})
if ($pools.Count -eq 0) { '[]' } else { $pools | ConvertTo-Json -Depth 3 -Compress }`

// rdsSessionScript usa qwinsta, que é a ferramenta nativa de enumeração de
// sessões (não depende do módulo RemoteDesktop, que pode não estar instalado).
// O parse é feito aqui para não depender de cmdlets do módulo RDS.
const rdsSessionScript = `$ErrorActionPreference = 'SilentlyContinue'
$out = @()
$prev = $null
foreach ($line in (qwinsta 2>$null)) {
    if ($line -notmatch '^(\s*)(\S+)\s+(\S+)\s+(\d+)\s+(\S+)') { continue }
    $sid = [int]$Matches[4]
    if ($sid -eq 0 -and $Matches[2] -eq 'services') { continue }
    $out += [PSCustomObject]@{
        SessionID = $sid
        Username  = [string]$Matches[3]
        State     = [string]$Matches[5]
        SessionType = $(if ($Matches[2] -eq 'console') { 'console' } else { 'rdp' })
    }
}
if ($out.Count -eq 0) { '[]' } else { $out | ConvertTo-Json -Depth 3 -Compress }`

// CollectStorageRDSInventario coleta pools de Storage Spaces e, se a máquina
// for Session Host, as sessões RDS abertas.
//
// Contrato: nunca erro, nunca panic. As duas coletas são independentes — a
// falha de uma não impede a outra.
func CollectStorageRDSInventario(ctx context.Context) StorageRDSInventory {
	if runtime.GOOS != "windows" {
		return StorageRDSInventory{Reason: "nao-windows"}
	}

	inv := StorageRDSInventory{}
	inv.StoragePools = collectStoragePools(ctx)

	// Session Host = serviço TermService presente. Checa antes de rodar
	// qwinsta: em estação de trabalho o resultado não teria valor.
	if serviceExists(serviceTerminalServices) {
		inv.RDSHost = true
		inv.RDSSessions = collectRDSSessions(ctx)
	}

	if len(inv.StoragePools) == 0 && !inv.RDSHost {
		inv.Reason = "sem-storage-spaces-e-sem-rds"
	}
	return inv
}

// collectStoragePools roda Get-StoragePool e converte em StoragePool.
// Devolve nil em qualquer falha — nunca erro.
func collectStoragePools(ctx context.Context) []StoragePool {
	poolCtx, cancel := context.WithTimeout(ctx, storageRDSTimeout)
	defer cancel()

	cmd := exec.CommandContext(poolCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", storagePoolScript)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return nil
	}

	type rawPool struct {
		Name              any `json:"Name"`
		HealthStatus      any `json:"HealthStatus"`
		OperationalStatus any `json:"OperationalStatus"`
		SizeGB            any `json:"SizeGB"`
		AllocatedGB       any `json:"AllocatedGB"`
		IsPrimordial      any `json:"IsPrimordial"`
		DiskCount         any `json:"DiskCount"`
	}

	var raw []rawPool
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		var single rawPool
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			return nil
		}
		raw = []rawPool{single}
	}

	var pools []StoragePool
	for _, r := range raw {
		if len(pools) >= maxStoragePools {
			break
		}
		name := strings.TrimSpace(wmiString(map[string]any{"v": r.Name}, "v"))
		if name == "" {
			continue
		}
		pools = append(pools, StoragePool{
			Name:              name,
			HealthStatus:      strings.TrimSpace(wmiString(map[string]any{"v": r.HealthStatus}, "v")),
			OperationalStatus: strings.TrimSpace(wmiString(map[string]any{"v": r.OperationalStatus}, "v")),
			SizeGB:            int64(wmiInt(map[string]any{"v": r.SizeGB}, "v")),
			AllocatedGB:       int64(wmiInt(map[string]any{"v": r.AllocatedGB}, "v")),
			DiskCount:         wmiInt(map[string]any{"v": r.DiskCount}, "v"),
			IsPrimordial:      wmiBool(map[string]any{"v": r.IsPrimordial}, "v"),
		})
	}
	return pools
}

// collectRDSSessions roda qwinsta e converte as sessões em RDSSession.
// Devolve nil em qualquer falha — nunca erro.
func collectRDSSessions(ctx context.Context) []RDSSession {
	rdsCtx, cancel := context.WithTimeout(ctx, storageRDSTimeout)
	defer cancel()

	// qwinsta não é um cmdlet, é um executável. Chamamos via PowerShell para
	// reaproveitar o parse em um só lugar e manter o padrão dos demais
	// coletores (saída JSON).
	cmd := exec.CommandContext(rdsCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", rdsSessionScript)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return nil
	}

	type rawSession struct {
		SessionID   any `json:"SessionID"`
		Username    any `json:"Username"`
		State       any `json:"State"`
		SessionType any `json:"SessionType"`
	}

	var raw []rawSession
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		var single rawSession
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			return nil
		}
		raw = []rawSession{single}
	}

	var sessions []RDSSession
	for _, r := range raw {
		if len(sessions) >= maxRDSSessions {
			break
		}
		id := wmiInt(map[string]any{"v": r.SessionID}, "v")
		// Sessão 0 é a de serviços; não é sessão de usuário e não interessa.
		if id == 0 {
			continue
		}
		sessions = append(sessions, RDSSession{
			SessionID:   id,
			Username:    strings.TrimSpace(wmiString(map[string]any{"v": r.Username}, "v")),
			State:       strings.TrimSpace(wmiString(map[string]any{"v": r.State}, "v")),
			SessionType: strings.TrimSpace(wmiString(map[string]any{"v": r.SessionType}, "v")),
		})
	}
	return sessions
}
