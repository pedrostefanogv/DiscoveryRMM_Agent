package app

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ─── Passo 2: Heartbeat de lease ────────────────────────────────────────────

// publishFetchHeartbeats publica heartbeats de lease para artifacts que este peer
// está ativamente buscando como fetcher. Só publica se a carga do host permitir.
func (c *p2pCoordinator) publishFetchHeartbeats(ctx context.Context) {
	if !c.isLoadOK() {
		return
	}

	selfAgentID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	if selfAgentID == "" {
		return
	}

	c.fetchStates.mu.Lock()
	defer c.fetchStates.mu.Unlock()

	now := time.Now()
	for artifactID, state := range c.fetchStates.states {
		if state.Status != "fetching" || !strings.EqualFold(state.OwnerPeerID, selfAgentID) {
			continue
		}
		if now.After(state.LeaseUntil) {
			state.Status = "failed"
			continue
		}

		// Renova o lease enquanto o fetch está em andamento. Sem isso, um
		// download longo (> artifactFetchLeaseTTL) expiraria o lease e outro
		// peer re-elegeria um fetcher, causando download duplicado/abortado.
		state.LeaseUntil = now.Add(artifactFetchLeaseTTL)

		hb := ArtifactFetchHeartbeat{
			ArtifactID:  artifactID,
			ClientID:    state.ClientID,
			OwnerPeerID: selfAgentID,
			Status:      state.Status,
			LeaseUntil:  state.LeaseUntil,
			ProgressPct: state.ProgressPct,
			UpdatedAt:   now.UTC(),
		}

		// Publicar via gossip — o canal exato depende do provider ativo.
		// No modo libp2p, publicamos via broadcast no tópico de fetch.
		c.publishFetchHeartbeatToGossip(ctx, hb)
	}
}

// publishFetchHeartbeatToGossip envia o heartbeat para o tópico de eleição.
// Quando o remote debug está ativo, o log é automaticamente capturado pelo
// subscriber de logs e enviado via NATS/WebSocket.
// Também envia o heartbeat para todos os peers conhecidos via libp2p para
// que renovem o lease remoto e evitem reeleição prematura.
func (c *p2pCoordinator) publishFetchHeartbeatToGossip(ctx context.Context, hb ArtifactFetchHeartbeat) {
	logLine := fmt.Sprintf("[p2p][fetch-hb] artifact=%s status=%s progress=%.0f%% lease=%s owner=%s",
		hb.ArtifactID, hb.Status, hb.ProgressPct,
		hb.LeaseUntil.Format(time.RFC3339), hb.OwnerPeerID)
	c.app.logs.append(logLine)

	// Broadcast para peers via libp2p
	if h, registry := c.libp2pHostAndRegistry(); h != nil && registry != nil {
		for _, agentID := range registry.AgentIDs() {
			if strings.EqualFold(strings.TrimSpace(agentID), strings.TrimSpace(hb.OwnerPeerID)) {
				continue
			}
			peerID, ok := registry.Lookup(agentID)
			if !ok {
				continue
			}
			go func(pid peer.ID) {
				if err := libp2pBroadcastFetchHeartbeat(ctx, h, pid, hb); err != nil {
					c.app.logs.append(fmt.Sprintf("[p2p][fetch-hb] falha broadcast para %s: %v", agentID, err))
				}
			}(peerID)
		}
	}
}

// ─── Passo 4: Processar candidaturas e eleger fetcher ───────────────────────

// handleFetchCandidacy processa uma candidatura de fetch recebida de um peer.
// Compara com a capacidade local via electBestFetcher e decide o vencedor.
// Deve ser chamado pelo handler de gossip quando receber ArtifactFetchCandidate.
func (c *p2pCoordinator) handleFetchCandidacy(ctx context.Context, msg ArtifactFetchCandidate) {
	artifactID := strings.TrimSpace(msg.ArtifactID)
	if artifactID == "" {
		return
	}

	selfAgentID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	if selfAgentID == "" {
		return
	}

	clientID := strings.TrimSpace(c.app.GetAgentConfiguration().ClientID)

	// Construir candidatura local
	load := c.collectHostLoad()
	selfCandidate := ArtifactFetchCandidate{
		ArtifactID: artifactID,
		ClientID:   clientID,
		AgentID:    selfAgentID,
		CPUCores:   load.CPUCores,
		RAMGB:      load.RamGB,
		CPUPercent: load.CPUPercent,
		MemPercent: load.MemoryPercent,
	}

	// Delegar a seleção para electBestFetcher
	winner := electBestFetcher(selfCandidate, []ArtifactFetchCandidate{msg})

	c.app.logs.append(fmt.Sprintf("[p2p][election] artifact=%s winner=%s (remote=%s)",
		artifactID, winner.AgentID, msg.AgentID))

	// Atualizar estado
	state := c.fetchStates.getOrCreate(artifactID, clientID)
	state.OwnerPeerID = winner.AgentID
	state.LeaseUntil = time.Now().Add(artifactFetchLeaseTTL)

	// Se este peer venceu e está apto, iniciar fetch
	if strings.EqualFold(winner.AgentID, selfAgentID) {
		if canStartLocalElection(state, time.Now(), c.isLoadOK()) {
			state.Status = "fetching"
			go c.executeFetch(ctx, artifactID, msg.ArtifactID)
		} else {
			state.Status = "missing"
			c.app.logs.append(fmt.Sprintf("[p2p][election] artifact=%s vencedor=local mas host sobrecarregado, adiando",
				artifactID))
		}
	} else {
		state.Status = "fetching"
		c.app.logs.append(fmt.Sprintf("[p2p][election] artifact=%s vencedor=remoto peer=%s",
			artifactID, winner.AgentID))
	}
}

// runLocalElection inicia uma eleição local para um artifact que está faltando.
// Publica a candidatura e aguarda respostas para decidir o fetcher.
func (c *p2pCoordinator) runLocalElection(ctx context.Context, artifactID string) {
	selfAgentID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	if selfAgentID == "" {
		return
	}

	// Verificar se o host está apto para participar
	if !c.isLoadOK() {
		c.app.logs.append(fmt.Sprintf("[p2p][election] artifact=%s host sobrecarregado, não participando da eleição",
			artifactID))
		return
	}

	clientID := strings.TrimSpace(c.app.GetAgentConfiguration().ClientID)
	load := c.collectHostLoad()

	candidate := ArtifactFetchCandidate{
		ArtifactID: artifactID,
		ClientID:   clientID,
		AgentID:    selfAgentID,
		CPUCores:   load.CPUCores,
		RAMGB:      load.RamGB,
		CPUPercent: load.CPUPercent,
		MemPercent: load.MemoryPercent,
	}

	// Publicar candidatura para todos os peers conhecidos via libp2p.
	// Isso permite que outros peers participem da eleição e que o melhor
	// candidato seja escolhido pelo electBestFetcher.
	if h, registry := c.libp2pHostAndRegistry(); h != nil && registry != nil {
		acceptedBy := libp2pBroadcastCandidacyToAll(ctx, h, registry, candidate)
		c.app.logs.append(fmt.Sprintf("[p2p][election] artifact=%s candidatura broadcast: %d peers aceitaram",
			artifactID, acceptedBy))
	} else {
		c.app.logs.append(fmt.Sprintf("[p2p][election] artifact=%s candidatura publicada (sem libp2p, apenas log) cpu=%d ram=%.1fGB cpuUse=%.1f%% memUse=%.1f%%",
			artifactID, candidate.CPUCores, candidate.RAMGB, candidate.CPUPercent, candidate.MemPercent))
	}

	// Período de graça: aguarda um curto intervalo após o broadcast para que
	// peers remotos processem a candidatura e reivindiquem o lease (via
	// heartbeat). Isso reduz a chance de múltiplos peers se auto-elegerem e
	// baixarem o mesmo artifact em paralelo.
	select {
	case <-ctx.Done():
		return
	case <-time.After(electionGracePeriod):
	}

	// Auto-eleição: se não houver outro peer com lease válido, este peer se elege.
	state := c.fetchStates.getOrCreate(artifactID, clientID)
	if canStartLocalElection(state, time.Now(), c.isLoadOK()) {
		state.OwnerPeerID = selfAgentID
		state.Status = "fetching"
		state.LeaseUntil = time.Now().Add(artifactFetchLeaseTTL)
		go c.executeFetch(ctx, artifactID, artifactID)
	}
}

// ─── Passo 5: Executar fetch quando eleito ──────────────────────────────────

// executeFetch executa o download do artifact quando este peer é eleito fetcher.
func (c *p2pCoordinator) executeFetch(ctx context.Context, artifactID string, artifactName string) {
	clientID := strings.TrimSpace(c.app.GetAgentConfiguration().ClientID)

	c.app.logs.append(fmt.Sprintf("[p2p][fetch] iniciando artifact=%s", artifactID))

	state := c.fetchStates.getOrCreate(artifactID, clientID)
	state.Status = "fetching"
	state.ProgressPct = 0
	c.fetchStates.set(artifactID, state)

	// O artifactName pode ser um GUID de release (artifactID) em vez do nome do
	// arquivo. Nesse caso, resolve o nome real a partir do índice de peers para
	// que downloadArtifactSwarm encontre o artifact corretamente.
	if strings.TrimSpace(artifactName) == "" || strings.EqualFold(strings.TrimSpace(artifactName), artifactID) {
		if resolved := c.resolveArtifactNameByID(artifactID); resolved != "" {
			artifactName = resolved
		}
	}

	// Tentar download via swarm (chunked de múltiplos peers)
	view, err := c.downloadArtifactSwarm(ctx, artifactName)

	// Recuperar estado atualizado após o download (pode ter sido alterado por heartbeat)
	state = c.fetchStates.getOrCreate(artifactID, clientID)
	if err != nil {
		state.Status = "failed"
		c.fetchStates.set(artifactID, state)
		c.app.logs.append(fmt.Sprintf("[p2p][fetch] artifact=%s falhou: %s", artifactID, err.Error()))
		return
	}

	state.Status = "available"
	state.ProgressPct = 100
	c.fetchStates.set(artifactID, state)

	c.app.logs.append(fmt.Sprintf("[p2p][fetch] artifact=%s concluido path=%s size=%d",
		artifactID, view.ArtifactName, view.SizeBytes))
}

// ─── Helpers de eleição ─────────────────────────────────────────────────────

// runPendingElections varre artifacts em estado "missing" e inicia eleição para cada um.
// Chamado periodicamente pelo loop principal do coordinator.
func (c *p2pCoordinator) runPendingElections(ctx context.Context) {
	c.fetchStates.mu.Lock()
	var pending []string
	for artifactID, state := range c.fetchStates.states {
		if state.Status == "missing" || state.Status == "failed" {
			pending = append(pending, artifactID)
		}
		// Expirar leases antigos
		if state.Status == "fetching" && time.Now().After(state.LeaseUntil) {
			state.Status = "missing"
			pending = append(pending, artifactID)
		}
	}
	c.fetchStates.mu.Unlock()

	for _, artifactID := range pending {
		c.runLocalElection(ctx, artifactID)
	}
}

// findMaxCPUAndRAM encontra os valores máximos de CPU cores e RAM GB em um grupo de candidatos.
func findMaxCPUAndRAM(candidates []ArtifactFetchCandidate) (maxCPU int, maxRAM float64) {
	for _, c := range candidates {
		if c.CPUCores > maxCPU {
			maxCPU = c.CPUCores
		}
		if c.RAMGB > maxRAM {
			maxRAM = c.RAMGB
		}
	}
	if maxCPU <= 0 {
		maxCPU = 1
	}
	if maxRAM <= 0 {
		maxRAM = 1
	}
	return
}

// artifactFetchCandidateScore emparelha um candidato com seu score calculado
// para ordenação durante eleição com múltiplos peers concorrentes.
type artifactFetchCandidateScore struct {
	candidate ArtifactFetchCandidate
	score     float64
}

// electBestFetcher seleciona o melhor fetcher entre um conjunto de candidatos.
// Scoreia todos, ordena por score decrescente (desempate por CPUCores) e retorna o vencedor.
func electBestFetcher(selfCandidate ArtifactFetchCandidate, remoteCandidates []ArtifactFetchCandidate) ArtifactFetchCandidate {
	all := append([]ArtifactFetchCandidate{selfCandidate}, remoteCandidates...)
	maxCPU, maxRAM := findMaxCPUAndRAM(all)

	scored := make([]artifactFetchCandidateScore, 0, len(all))
	for _, cand := range all {
		scored = append(scored, artifactFetchCandidateScore{
			candidate: cand,
			score:     computeScore(cand, maxCPU, maxRAM),
		})
	}

	// Ordenar por score decrescente; em empate, prefere mais CPUCores
	sort.SliceStable(scored, func(i, j int) bool {
		if math.Abs(scored[i].score-scored[j].score) > 0.001 {
			return scored[i].score > scored[j].score
		}
		return scored[i].candidate.CPUCores > scored[j].candidate.CPUCores
	})

	return scored[0].candidate
}
