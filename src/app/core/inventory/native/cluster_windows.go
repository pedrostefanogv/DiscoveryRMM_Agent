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
// F12 — Failover Clustering: nós, recursos e quorum.
//
// Coletor CONDICIONAL: só roda quando o serviço ClusSvc existe (role Failover
// Clustering instalada). Sem a role, devolve vazio com motivo e não chama
// PowerShell.
//
// SOMENTE-LEITURA: usamos apenas Get-Cluster, Get-ClusterNode e
// Get-ClusterResource (verbos de consulta). Nenhum Start/Stop/Move/Failover —
// mexer no estado de um cluster de produção é ação de operador, não de
// inventário.
//
// O quorum é o dado mais importante: um cluster sem quorum está fora do ar
// mesmo com os nós ligados, e é o modo de falha que passa despercebido.
// ─────────────────────────────────────────────────────────────────────────────

// clusterTimeout limita a coleta. Get-ClusterResource enumera TODOS os
// recursos (centenas em cluster grande) e é a parte mais lenta.
const clusterTimeout = 25 * time.Second

// maxClusterResources limita quantos recursos entram no payload.
const maxClusterResources = 400

// ClusterInventory é o resultado do F12.
type ClusterInventory struct {
	// Present indica que a role Failover Clustering está instalada.
	Present bool `json:"present"`
	// Clusters lista os clusters locais (normalmente um por nó).
	Clusters []ClusterInfo `json:"clusters,omitempty"`
	// Reason explica resultado vazio.
	Reason string `json:"reason,omitempty"`
}

// ClusterInfo descreve o cluster visto deste nó.
type ClusterInfo struct {
	Name string `json:"name,omitempty"`
	// Nodes são os nós com estado e voto.
	Nodes []ClusterNode `json:"nodes,omitempty"`
	// Resources são os recursos gerenciados, com estado e grupo.
	Resources []ClusterResource `json:"resources,omitempty"`
	// QuorumType / QuorumResource descrevem a configuração de quórum.
	QuorumType     string `json:"quorumType,omitempty"`
	QuorumResource string `json:"quorumResource,omitempty"`
	// DegradedResources conta recursos que NÃO estão Online — o sinal
	// acionável. Calculado no agente para o servidor não reparsear.
	DegradedResources int `json:"degradedResources,omitempty"`
}

// ClusterNode é um nó do cluster.
type ClusterNode struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
	// Vote informa se o nó participa do quórum.
	Vote string `json:"vote,omitempty"`
}

// ClusterResource é um recurso do cluster.
type ClusterResource struct {
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
	Group string `json:"group,omitempty"`
	Type  string `json:"type,omitempty"`
}

// clusterIsDegradedState informa se o estado de um recurso indica problema.
// É PURA. "Online" é a única condição saudável de um recurso de cluster;
// "OnlinePending" ainda é transição, mas já sinaliza atenção.
func clusterIsDegradedState(state string) bool {
	trimmed := strings.TrimSpace(state)
	if trimmed == "" {
		return false // estado desconhecido não é degradado — não inferimos
	}
	return !strings.EqualFold(trimmed, "Online")
}

// clusterScript coleta nós, recursos e quórum em UMA chamada de PowerShell.
// Uma chamada só evita pagar três inicializações do PowerShell (caro) e
// mantém o snapshot consistente entre as três consultas.
const clusterScript = `$ErrorActionPreference = 'SilentlyContinue'
$out = [PSCustomObject]@{
    Clusters = @()
}
$clusters = @()
foreach ($c in @(Get-Cluster)) {
    $nodes = @(Get-ClusterNode -Cluster $c.Name | ForEach-Object {
        [PSCustomObject]@{
            Name  = $_.Name
            State = [string]$_.State
            Vote  = [string]$_.DynamicWeight
        }
    })
    $resources = @(Get-ClusterResource -Cluster $c.Name | Select-Object -First 400 | ForEach-Object {
        [PSCustomObject]@{
            Name  = $_.Name
            State = [string]$_.State
            Group = [string]$_.OwnerGroup
            Type  = [string]$_.ResourceType
        }
    })
    $q = Get-ClusterQuorum -Cluster $c.Name
    $clusters += [PSCustomObject]@{
        Name           = $c.Name
        Nodes          = $nodes
        Resources      = $resources
        QuorumType     = [string]$q.QuorumType
        QuorumResource = [string]$q.QuorumResource
    }
}
if ($clusters.Count -eq 0) { '[]' } else { $clusters | ConvertTo-Json -Depth 6 -Compress }`

// CollectClusterInventario coleta o inventário do Failover Cluster.
//
// Contrato: nunca erro, nunca panic. Sem a role devolve Present=false com
// motivo. Falha de execução/parse degrada para vazio com motivo.
func CollectClusterInventario(ctx context.Context) ClusterInventory {
	if runtime.GOOS != "windows" {
		return ClusterInventory{Reason: "nao-windows"}
	}
	if !serviceExists(serviceFailoverCluster) {
		return ClusterInventory{Reason: "failover-clustering-ausente"}
	}

	inv := ClusterInventory{Present: true}

	clusterCtx, cancel := context.WithTimeout(ctx, clusterTimeout)
	defer cancel()

	cmd := exec.CommandContext(clusterCtx, "powershell",
		"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden",
		"-Command", clusterScript)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if clusterCtx.Err() == context.DeadlineExceeded {
			inv.Reason = "timeout"
		} else {
			inv.Reason = "falha-ao-executar-powershell"
		}
		return inv
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		// A role existe mas nenhum cluster está configurado neste nó.
		inv.Reason = "sem-cluster-configurado"
		return inv
	}

	type rawNode struct {
		Name  string `json:"Name"`
		State any    `json:"State"`
		Vote  any    `json:"Vote"`
	}
	type rawResource struct {
		Name  string `json:"Name"`
		State any    `json:"State"`
		Group any    `json:"Group"`
		Type  any    `json:"Type"`
	}
	type rawCluster struct {
		Name           any           `json:"Name"`
		Nodes          []rawNode     `json:"Nodes"`
		Resources      []rawResource `json:"Resources"`
		QuorumType     any           `json:"QuorumType"`
		QuorumResource any           `json:"QuorumResource"`
	}

	var raw []rawCluster
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		var single rawCluster
		if err2 := json.Unmarshal([]byte(trimmed), &single); err2 != nil {
			inv.Reason = "falha-ao-parsear"
			return inv
		}
		raw = []rawCluster{single}
	}

	for _, rc := range raw {
		info := ClusterInfo{
			Name:           strings.TrimSpace(wmiString(map[string]any{"v": rc.Name}, "v")),
			QuorumType:     strings.TrimSpace(wmiString(map[string]any{"v": rc.QuorumType}, "v")),
			QuorumResource: strings.TrimSpace(wmiString(map[string]any{"v": rc.QuorumResource}, "v")),
		}
		for _, rn := range rc.Nodes {
			name := strings.TrimSpace(wmiString(map[string]any{"v": rn.Name}, "v"))
			if name == "" {
				continue
			}
			info.Nodes = append(info.Nodes, ClusterNode{
				Name:  name,
				State: strings.TrimSpace(wmiString(map[string]any{"v": rn.State}, "v")),
				Vote:  strings.TrimSpace(wmiString(map[string]any{"v": rn.Vote}, "v")),
			})
		}
		for _, rr := range rc.Resources {
			if len(info.Resources) >= maxClusterResources {
				break
			}
			name := strings.TrimSpace(wmiString(map[string]any{"v": rr.Name}, "v"))
			if name == "" {
				continue
			}
			state := strings.TrimSpace(wmiString(map[string]any{"v": rr.State}, "v"))
			if clusterIsDegradedState(state) {
				info.DegradedResources++
			}
			info.Resources = append(info.Resources, ClusterResource{
				Name:  name,
				State: state,
				Group: strings.TrimSpace(wmiString(map[string]any{"v": rr.Group}, "v")),
				Type:  strings.TrimSpace(wmiString(map[string]any{"v": rr.Type}, "v")),
			})
		}
		inv.Clusters = append(inv.Clusters, info)
	}

	if len(inv.Clusters) == 0 {
		inv.Reason = "sem-cluster-configurado"
	}
	return inv
}
