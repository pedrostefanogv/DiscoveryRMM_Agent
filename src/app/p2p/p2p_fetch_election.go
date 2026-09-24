package p2p

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
func (c *Coordinator) publishFetchHeartbeats(ctx context.Context) {
	if !c.isLoadOK() {
		return
	}

	selfAgentID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	if selfAgentID == "" {
		return
	}

	// Coleta os heartbeats sob o lock e publica FORA dele. O broadcast libp2p
	// chama libp2pHostAndRegistry (que adquire c.mu); publicar dentro do lock
	// segurava fetchStates.mu por toda a rodada e criava ordem de lock.
	c.fetchStates.mu.Lock()
	now := time.Now()
	heartbeats := make([]ArtifactFetchHeartbeat, 0, len(c.fetchStates.states))
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

		heartbeats = append(heartbeats, ArtifactFetchHeartbeat{
			ArtifactID:  artifactID,
			ClientID:    state.ClientID,
			OwnerPeerID: selfAgentID,
			Status:      state.Status,
			LeaseUntil:  state.LeaseUntil,
			ProgressPct: state.ProgressPct,
			UpdatedAt:   now.UTC(),
		})

	}
	c.fetchStates.mu.Unlock()

	// Publicar via gossip — o canal exato depende do provider ativo.
	// No modo libp2p, publicamos via broadcast no tópico de fetch.
	for _, hb := range heartbeats {
		c.publishFetchHeartbeatToGossip(ctx, hb)
	}
}

// publishFetchHeartbeatToGossip envia o heartbeat para o tópico de eleição.
// Quando o remote debug está ativo, o log é automaticamente capturado pelo
// subscriber de logs e enviado via NATS/WebSocket.
// Também envia o heartbeat para todos os peers conhecidos via libp2p para
// que renovem o lease remoto e evitem reeleição prematura.
func (c *Coordinator) publishFetchHeartbeatToGossip(ctx context.Context, hb ArtifactFetchHeartbeat) {
	logLine := fmt.Sprintf("[p2p][fetch-hb] artifact=%s status=%s progress=%.0f%% lease=%s owner=%s",
		hb.ArtifactID, hb.Status, hb.ProgressPct,
		hb.LeaseUntil.Format(time.RFC3339), hb.OwnerPeerID)
	c.deps.Log(logLine)

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
					c.deps.Log(fmt.Sprintf("[p2p][fetch-hb] falha broadcast para %s: %v", agentID, err))
				}
			}(peerID)
		}
	}
}

// ─── Passo 4: Processar candidaturas e eleger fetcher ───────────────────────

// handleFetchCandidacy processa uma candidatura de fetch recebida de um peer.
// Compara com a capacidade local via electBestFetcher e decide o vencedor.
// Deve ser chamado pelo handler de gossip quando receber ArtifactFetchCandidate.
func (c *Coordinator) handleFetchCandidacy(ctx context.Context, msg ArtifactFetchCandidate) {
	artifactID := strings.TrimSpace(msg.ArtifactID)
	if artifactID == "" {
		return
	}

	selfAgentID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	if selfAgentID == "" {
		return
	}

	clientID := strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID)

	// Já temos o artifact localmente: não compete na eleição nem sobrescreve o
	// estado "available". Sem esta guarda, uma candidatura remota rebaixava o
	// estado para "missing"/"fetching" e o re-seed re-baixava o arquivo.
	if c.artifactPresentLocally(artifactID, c.resolveArtifactNameByID(artifactID)) {
		c.fetchStates.mutate(artifactID, clientID, func(state *ArtifactFetchState) {
			state.Status = "available"
			state.ProgressPct = 100
			state.OwnerPeerID = selfAgentID
			state.FailCount = 0
			state.NextAttemptUTC = time.Time{}
		})
		return
	}

	// Construir candidatura local
	load := c.CollectHostLoad()
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

	c.deps.Log(fmt.Sprintf("[p2p][election] artifact=%s winner=%s (remote=%s)",
		artifactID, winner.AgentID, msg.AgentID))

	// Atualizar estado (A7: mutações sob lock via fetchStates.mutate)
	var startFetch bool
	c.fetchStates.mutate(artifactID, clientID, func(state *ArtifactFetchState) {
		state.OwnerPeerID = winner.AgentID
		state.LeaseUntil = time.Now().Add(artifactFetchLeaseTTL)
		if strings.EqualFold(winner.AgentID, selfAgentID) {
			if canStartLocalElection(state, time.Now(), c.isLoadOK()) {
				state.Status = "fetching"
				startFetch = true
			} else {
				state.Status = "missing"
			}
		} else {
			state.Status = "fetching"
		}
	})

	// Se este peer venceu e está apto, iniciar fetch
	if strings.EqualFold(winner.AgentID, selfAgentID) {
		if startFetch {
			go c.executeFetch(ctx, artifactID, msg.ArtifactID)
		} else {
			c.deps.Log(fmt.Sprintf("[p2p][election] artifact=%s vencedor=local mas host sobrecarregado, adiando",
				artifactID))
		}
	} else {
		c.deps.Log(fmt.Sprintf("[p2p][election] artifact=%s vencedor=remoto peer=%s",
			artifactID, winner.AgentID))
	}
}

// runLocalElection inicia uma eleição local para um artifact que está faltando.
// Publica a candidatura e aguarda respostas para decidir o fetcher.
func (c *Coordinator) runLocalElection(ctx context.Context, artifactID string) {
	selfAgentID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	if selfAgentID == "" {
		return
	}

	// Verificar se o host está apto para participar
	if !c.isLoadOK() {
		c.deps.Log(fmt.Sprintf("[p2p][election] artifact=%s host sobrecarregado, não participando da eleição",
			artifactID))
		return
	}

	clientID := strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID)

	// Se o artifact já está em disco, não re-elege: o ID local pode divergir do
	// ID anunciado pelos peers (download P2P sem sidecar .meta), e re-eleger
	// re-baixaria o mesmo arquivo a cada expiração de lease.
	if c.artifactPresentLocally(artifactID, c.resolveArtifactNameByID(artifactID)) {
		c.fetchStates.mutate(artifactID, clientID, func(state *ArtifactFetchState) {
			state.Status = "available"
			state.ProgressPct = 100
			state.FailCount = 0
			state.NextAttemptUTC = time.Time{}
		})
		return
	}

	// Respeita o backoff/cooldown vigente antes de iniciar nova rodada de
	// eleição: o re-seed chama runLocalElection direto e, sem esta guarda,
	// ignorava o NextAttemptUTC gravado em falha ou após um fetch bem-sucedido.
	if snap, ok := c.fetchStates.snapshot(artifactID); ok {
		if !canStartLocalElection(&snap, time.Now(), c.isLoadOK()) {
			return
		}
	}

	load := c.CollectHostLoad()

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
		c.deps.Log(fmt.Sprintf("[p2p][election] artifact=%s candidatura broadcast: %d peers aceitaram",
			artifactID, acceptedBy))
	} else {
		c.deps.Log(fmt.Sprintf("[p2p][election] artifact=%s candidatura publicada (sem libp2p, apenas log) cpu=%d ram=%.1fGB cpuUse=%.1f%% memUse=%.1f%%",
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

	// Auto-eleição: se não houver outro peer com lease válido, este peer se
	// elege. A checagem e a reivindicação acontecem na MESMA seção crítica
	// (fetchStates.mutate): sem isso, re-seed + ticker de pending eleições
	// podiam passar juntos pelo check-then-set e disparar downloads duplicados
	// do mesmo artifact (além de mutar campos fora do lock — A7).
	var claimed bool
	c.fetchStates.mutate(artifactID, clientID, func(state *ArtifactFetchState) {
		if !canStartLocalElection(state, time.Now(), c.isLoadOK()) {
			return
		}
		state.OwnerPeerID = selfAgentID
		state.Status = "fetching"
		state.LeaseUntil = time.Now().Add(artifactFetchLeaseTTL)
		claimed = true
	})
	if claimed {
		go c.executeFetch(ctx, artifactID, artifactID)
	}
}

// ─── Passo 5: Executar fetch quando eleito ──────────────────────────────────

// executeFetch executa o download do artifact quando este peer é eleito fetcher.
// A7: todas as mutações de estado passam por fetchStates.mutate (sob lock).
func (c *Coordinator) executeFetch(ctx context.Context, artifactID string, artifactName string) {
	clientID := strings.TrimSpace(c.deps.GetAgentConfiguration().ClientID)

	c.deps.Log(fmt.Sprintf("[p2p][fetch] iniciando artifact=%s", artifactID))

	c.fetchStates.mutate(artifactID, clientID, func(state *ArtifactFetchState) {
		state.Status = "fetching"
		state.ProgressPct = 0
	})

	// O artifactName pode ser um GUID de release (artifactID) em vez do nome do
	// arquivo. Nesse caso, resolve o nome real a partir do índice de peers para
	// que downloadArtifactSwarm encontre o artifact corretamente.
	if strings.TrimSpace(artifactName) == "" || strings.EqualFold(strings.TrimSpace(artifactName), artifactID) {
		if resolved := c.resolveArtifactNameByID(artifactID); resolved != "" {
			artifactName = resolved
		}
	}

	// Tentar download via swarm (chunked de múltiplos peers)
	view, err := c.DownloadArtifactSwarm(ctx, artifactName)

	if err != nil {
		// M12: backoff exponencial por falha consecutiva (1m, 2m, 4m, … teto 30m).
		var failCount int
		var next time.Time
		c.fetchStates.mutate(artifactID, clientID, func(state *ArtifactFetchState) {
			state.Status = "failed"
			state.FailCount++
			failCount = state.FailCount
			backoff := time.Minute << (min(failCount-1, 5))
			if backoff > 30*time.Minute {
				backoff = 30 * time.Minute
			}
			state.NextAttemptUTC = time.Now().UTC().Add(backoff)
			next = state.NextAttemptUTC
		})
		c.deps.Log(fmt.Sprintf("[p2p][fetch] artifact=%s falhou (tentativa %d): %s — próxima tentativa em %s", artifactID, failCount, err.Error(), next.UTC().Format(time.RFC3339)))
		return
	}

	c.fetchStates.mutate(artifactID, clientID, func(state *ArtifactFetchState) {
		state.Status = "available"
		state.ProgressPct = 100
		state.FailCount = 0
		// Cooldown pós-sucesso: se o artifact voltar a ser marcado "missing"
		// (lease expirado, candidatura remota), o re-seed só o re-baixa depois
		// desta janela — defesa extra contra o loop de re-download.
		state.NextAttemptUTC = time.Now().UTC().Add(artifactFetchSuccessCooldown)
	})

	// Persiste o artifactID lógico no sidecar .meta. Sem isso, ListArtifacts
	// deriva "name:<arquivo>" e o re-seed deixa de reconhecer que este nó já
	// possui o artifact — origem do loop de re-download/re-seed.
	c.recordArtifactIdentity(view.ArtifactName, artifactID)

	c.deps.Log(fmt.Sprintf("[p2p][fetch] artifact=%s concluido path=%s size=%d",
		artifactID, view.ArtifactName, view.SizeBytes))
}

// ─── Helpers de eleição ─────────────────────────────────────────────────────

// runPendingElections varre artifacts em estado "missing" e inicia eleição para cada um.
// Chamado periodicamente pelo loop principal do coordinator.
func (c *Coordinator) runPendingElections(ctx context.Context) {
	c.fetchStates.mu.Lock()
	now := time.Now()
	var pending []string
	for artifactID, state := range c.fetchStates.states {
		if state.Status == "missing" || state.Status == "failed" {
			// M12: respeita o backoff de re-eleição (evita re-eleger e baixar
			// a cada 60s indefinidamente um artifact que continua falhando).
			if now.Before(state.NextAttemptUTC) {
				continue
			}
			pending = append(pending, artifactID)
		}
		// Expirar leases antigos
		if state.Status == "fetching" && now.After(state.LeaseUntil) {
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
