package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"discovery/app/core/automation"
)

// ─── Comando p2ppreload (servidor → agent) ──────────────────────────────────
//
// O servidor solicita pré-carga P2P de packages. Ao receber o comando, TODOS
// os agentes do escopo o recebem, mas apenas o de MAIOR SCORE baixa primeiro:
//
//   1. O agent verifica o estado real (não instalado / update pendente).
//   2. Espera um atraso proporcional à sua posição no score local:
//      quanto melhor a máquina (CPU/RAM livres), MENOR o atraso.
//   3. Antes de baixar, checa se o artifact já apareceu na rede P2P
//      (o agente com melhor score já terminou). Se sim, baixa da LAN.
//   4. Se ninguém publicou ainda, baixa via winget e publica — virando seed.
//
// Resultado: download coordenado em partes (o melhor faz primeiro, os outros
// propagam), sem tempestade de downloads simultâneos da internet.

const (
	// p2pPreloadMaxStagger é o teto de atraso do stagger por score.
	p2pPreloadMaxStagger = 90 * time.Second

	// p2pPreloadNetworkCheckInterval intervalo entre checagens do índice P2P
	// durante o stagger (o artifact pode chegar via peer a qualquer momento).
	p2pPreloadNetworkCheckInterval = 5 * time.Second
)

// p2pPreloadPackage é um item do payload do comando p2ppreload.
type p2pPreloadPackage struct {
	PackageID  string `json:"packageId"`
	ActionType string `json:"actionType,omitempty"`
}

type p2pPreloadPayload struct {
	Action   string              `json:"action"`
	Packages []p2pPreloadPackage `json:"packages"`
}

// handleP2pPreloadCommand processa o comando de pré-carga.
// Retorna (handled=true, exitCode, output, errText).
func (a *App) handleP2pPreloadCommand(parent context.Context, payload any) (bool, int, string, string) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return true, 2, "", fmt.Sprintf("p2ppreload: payload invalido: %v", err)
	}
	var req p2pPreloadPayload
	if err := json.Unmarshal(raw, &req); err != nil {
		return true, 2, "", fmt.Sprintf("p2ppreload: payload invalido: %v", err)
	}
	if strings.EqualFold(strings.TrimSpace(req.Action), "cancel") {
		return true, 0, "p2ppreload: cancel recebido (itens em andamento continuam; novos itens nao iniciam)", ""
	}
	if len(req.Packages) == 0 {
		return true, 2, "", "p2ppreload: payload sem packages"
	}
	if a.packageManagerRouter == nil || a.p2pCoord == nil {
		return true, 2, "", "p2ppreload: P2P indisponivel neste agent"
	}

	// Executa em background — o comando retorna imediatamente (o download
	// pode demorar minutos; o outbox de resultados registra o ack/resultado
	// deste retorno, o progresso vai por log/telemetria).
	go a.runP2pPreload(parent, req.Packages)

	return true, 0, "p2ppreload: agendado (execucao em background com stagger por score)", ""
}

// runP2pPreload executa a pré-carga com stagger por score.
func (a *App) runP2pPreload(parent context.Context, packages []p2pPreloadPackage) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()

	for _, pkg := range packages {
		if ctx.Err() != nil {
			return
		}
		packageID := strings.TrimSpace(pkg.PackageID)
		if packageID == "" {
			continue
		}

		// 1. Estado real da máquina: pacote em estado final → nada a fazer.
		if !automation.ShouldPreloadPackage(ctx, a.packageManagerRouter, automation.AutomationTaskActionType(strings.TrimSpace(pkg.ActionType)), packageID) {
			a.logs.append(fmt.Sprintf("[p2p][preload] pacote em estado final, ignorando packageId=%s action=%s", packageID, pkg.ActionType))
			continue
		}

		// 2. Stagger por score: melhor máquina baixa primeiro.
		if !a.waitPreloadTurn(ctx, packageID) {
			return
		}

		// 3. Download coordenado.
		a.packageManagerRouter.PreloadPackageForP2P(ctx, packageID)
	}
}

// waitPreloadTurn espera a vez deste agent no stagger. Retorna false se o ctx
// foi cancelado. Durante a espera, monitora o índice P2P: se o artifact já
// foi publicado por outro agent (de melhor score), encerra a espera antes —
// este agent vai baixar da LAN em vez da internet.
func (a *App) waitPreloadTurn(ctx context.Context, packageID string) bool {
	score, hasScore := a.localPreloadScore()
	if !hasScore {
		// Sem P2P host ativo: baixa direto (não há coordenação possível).
		return true
	}

	delay := preloadStaggerDelay(score)
	a.logs.append(fmt.Sprintf("[p2p][preload] aguardando vez (score=%.2f delay=%s) packageId=%s", score, delay.Round(time.Second), packageID))

	timer := time.NewTimer(delay)
	defer timer.Stop()
	tick := time.NewTicker(p2pPreloadNetworkCheckInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-tick.C:
			// O agent de maior score já publicou? Então nossa vez chegou —
			// baixando da LAN em vez da internet.
			if a.artifactAvailableOnNetwork(packageID) {
				a.logs.append(fmt.Sprintf("[p2p][preload] artifact disponível na rede, antecipando download packageId=%s", packageID))
				return true
			}
		}
	}
}

// localPreloadScore calcula o score de capacidade local (mesma fórmula da
// eleição de fetcher: CPU/RAM livres normalizados). Retorna (score, ok).
func (a *App) localPreloadScore() (float64, bool) {
	if a.p2pCoord == nil {
		return 0, false
	}
	load := a.p2pCoord.CollectHostLoad()
	if load.CPUCores <= 0 {
		load.CPUCores = 1
	}
	if load.RamGB <= 0 {
		load.RamGB = 1
	}
	coresFree := float64(load.CPUCores) * (100 - load.CPUPercent) / 100
	ramFree := load.RamGB * (100 - load.MemoryPercent) / 100
	return coresFree/2 + ramFree/8, true
}

// preloadStaggerDelay converte o score em atraso: score alto (máquina potente/
// livre) → delay pequeno (baixa primeiro); score baixo → delay grande.
// score típico: 0 (saturado) a ~10 (máquina grande ociosa).
func preloadStaggerDelay(score float64) time.Duration {
	if score < 0 {
		score = 0
	}
	if score > 10 {
		score = 10
	}
	// delay = maxStagger * (1 - score/10): score 10 → 0s; score 0 → 90s.
	return time.Duration(float64(p2pPreloadMaxStagger) * (1 - score/10))
}

// artifactAvailableOnNetwork verifica se algum peer já anunciou o artifact.
func (a *App) artifactAvailableOnNetwork(packageID string) bool {
	artifactID := "winget:" + normalizePackageLookupKey(packageID)
	if a.p2pCoord == nil || artifactID == "winget:" {
		return false
	}
	peers := a.p2pCoord.PeersWithArtifactScored(artifactID)
	return len(peers) > 0
}
