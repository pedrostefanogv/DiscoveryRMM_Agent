package p2p

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// ─── Re-seed automático (cobertura garantida de artifacts) ──────────────────
//
// Objetivo: TODO agent mantém os artifacts que circulam no site (todos são
// seed em potencial). Quando o índice de gossip mostra que um artifact existe
// em peers mas NÃO localmente, este peer entra na eleição de fetcher
// (p2p_fetch_election.go) para baixá-lo e republicá-lo — restaurando a
// redundância sem intervenção do servidor.
//
// Isso cobre os cenários:
//   - seed original desligou/perdeu rede → restantes re-povoam;
//   - agent novo entra no site → adota o catálogo que já circula;
//   - site em deploy progressivo → cada artifact que entra na rede se
//     propaga para todos, aumentando a redundância a cada agent.

const (
	// reseedScanInterval define a frequência da varredura de cobertura.
	// Alinhado com o gossip (45s): a cada ciclo, com offset, reavalia.
	reseedScanInterval = 2 * time.Minute

	// reseedMaxFetchesPerCycle limita downloads disparados por ciclo para
	// não saturar a rede/hosts (artifacts grandes são chunked).
	reseedMaxFetchesPerCycle = 2

	// reseedMinAge evita re-agir sobre anúncios recém-vistos durante
	// downloads já em andamento de outros peers.
	reseedMinAnnounceAge = 0 * time.Second
)

// ScanAndSeedMissingArtifacts compara o índice de artifacts dos peers com o
// cache local. Artifacts presentes na rede mas ausentes localmente entram no
// pipeline de eleição de fetcher (fetchStates → runPendingElections), que
// decide quem baixa (score de capacidade) — evitando que todos baixem junto.
//
// Retorna a lista de artifactIDs registrados como missing neste ciclo.
func (c *Coordinator) ScanAndSeedMissingArtifacts(ctx context.Context) []string {
	if c == nil || c.deps == nil {
		return nil
	}
	cfg := c.deps.GetP2PConfig()
	if !cfg.Enabled {
		return nil
	}
	// Somente peers com libp2p ativo participam — a eleição usa broadcast
	// libp2p para coordenação (sem ele, cada agent decidiria sozinho).
	h, registry := c.libp2pHostAndRegistry()
	if h == nil || registry == nil {
		return nil
	}

	selfAgentID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	if selfAgentID == "" {
		return nil
	}

	// 1. Artifacts anunciados pelos peers (cache de gossip, TTL 72h).
	c.mu.RLock()
	announced := make(map[string]string) // artifactID -> artifactName
	for _, state := range c.peerArtifacts {
		for _, artifact := range state.Artifacts {
			id := strings.TrimSpace(artifact.ArtifactID)
			if id == "" {
				continue
			}
			if _, exists := announced[id]; !exists {
				announced[id] = strings.TrimSpace(artifact.ArtifactName)
			}
		}
	}
	c.mu.RUnlock()
	if len(announced) == 0 {
		return nil
	}

	// 2. Cache local.
	local := make(map[string]bool)
	if artifacts, err := c.ListArtifacts(); err == nil {
		for _, a := range artifacts {
			local[strings.TrimSpace(a.ArtifactID)] = true
		}
	}

	clientID := strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID)
	var seeded []string
	fetches := 0

	for artifactID, artifactName := range announced {
		if local[artifactID] {
			continue // já temos — já somos seed deste artifact
		}

		// Estado de fetch: "missing" aciona runPendingElections (ticker de
		// 60s do coordinator), que roda runLocalElection → broadcast de
		// candidatura → electBestFetcher (score CPU/RAM) → executeFetch
		// (download via swarm) → republica localmente. Um por vez: se
		// já existe fetch em andamento/lease válido, nada muda.
		state := c.fetchStates.getOrCreate(artifactID, clientID)
		if state.Status == "fetching" || state.Status == "available" {
			continue
		}
		if state.Status == "" || state.Status == "missing" || state.Status == "failed" {
			if fetches >= reseedMaxFetchesPerCycle {
				continue
			}
			fetches++
			seeded = append(seeded, artifactID)
			c.deps.Log(fmt.Sprintf("[p2p][reseed] artifact na rede mas ausente localmente, entrando na eleição artifactID=%s name=%s", artifactID, artifactName))
			// Dispara eleição imediata (não espera o ticker de pending).
			go c.runLocalElection(ctx, artifactID)
		}
	}

	return seeded
}

// startReseedLoop roda a varredura de cobertura em background.
// Chamado pelo Run do coordinator.
func (c *Coordinator) startReseedLoop(ctx context.Context) {
	ticker := time.NewTicker(reseedScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Jitter leve para dessincronizar agentes que ligaram juntos.
			select {
			case <-ctx.Done():
				return
			case <-time.After(reseedJitter()):
			}
			_ = c.ScanAndSeedMissingArtifacts(ctx)
		}
	}
}

// reseedJitter retorna um atraso aleatório de 0-10s.
func reseedJitter() time.Duration {
	return time.Duration(rand.Int63n(int64(10 * time.Second)))
}
