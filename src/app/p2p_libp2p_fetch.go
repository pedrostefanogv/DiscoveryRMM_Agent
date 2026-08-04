package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ── /fetch handlers ─────────────────────────────────────────────────────────

// handleStreamFetchCandidacy processa uma candidatura de fetch recebida via libp2p.
// Decodifica o candidato, chama handleFetchCandidacy no coordinator e responde.
func handleStreamFetchCandidacy(s network.Stream, coord *p2pCoordinator) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pCandidacyTimeout))

	var req libp2pCandidacyRequest
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&req); err != nil {
		_ = json.NewEncoder(s).Encode(libp2pCandidacyResponse{Accepted: false, Message: "payload inválido"})
		return
	}

	req.ArtifactID = strings.TrimSpace(req.ArtifactID)
	req.AgentID = strings.TrimSpace(req.AgentID)
	if req.ArtifactID == "" || req.AgentID == "" {
		_ = json.NewEncoder(s).Encode(libp2pCandidacyResponse{Accepted: false, Message: "artifactID ou agentID ausente"})
		return
	}

	// Processar candidatura (eleição local)
	if coord != nil && coord.deps != nil {
		coord.handleFetchCandidacy(context.Background(), ArtifactFetchCandidate(req))
	}

	_ = json.NewEncoder(s).Encode(libp2pCandidacyResponse{Accepted: true})
}

// handleStreamFetchHeartbeat processa um heartbeat de fetch recebido via libp2p.
// Atualiza o lease remoto para evitar reeleição prematura.
func handleStreamFetchHeartbeat(s network.Stream, coord *p2pCoordinator) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	var hb ArtifactFetchHeartbeat
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&hb); err != nil {
		return
	}

	hb.ArtifactID = strings.TrimSpace(hb.ArtifactID)
	hb.OwnerPeerID = strings.TrimSpace(hb.OwnerPeerID)
	if hb.ArtifactID == "" || hb.OwnerPeerID == "" {
		return
	}

	// Se o heartbeat é de outro peer, renovar o lease remoto no fetchStates
	if coord != nil && coord.deps != nil {
		selfAgentID := strings.TrimSpace(coord.deps.GetDebugConfig().AgentID)
		if !strings.EqualFold(hb.OwnerPeerID, selfAgentID) {
			clientID := strings.TrimSpace(coord.deps.GetAgentConfiguration().ClientID)
			state := coord.fetchStates.getOrCreate(hb.ArtifactID, clientID)
			state.OwnerPeerID = hb.OwnerPeerID
			state.Status = hb.Status
			state.LeaseUntil = hb.LeaseUntil
			if hb.ProgressPct > state.ProgressPct {
				state.ProgressPct = hb.ProgressPct
			}
		}
	}
}

// ── Client-side: broadcast de candidatura e heartbeat ───────────────────────

// libp2pBroadcastCandidacy envia uma candidatura de fetch para um peer específico.
func libp2pBroadcastCandidacy(ctx context.Context, h host.Host, peerID peer.ID, candidate ArtifactFetchCandidate) (bool, error) {
	s, err := h.NewStream(ctx, peerID, protoFetchCandidacy)
	if err != nil {
		return false, fmt.Errorf("stream candidacy: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pCandidacyTimeout))

	req := libp2pCandidacyRequest(candidate)
	if err := json.NewEncoder(s).Encode(req); err != nil {
		return false, fmt.Errorf("encode candidacy: %w", err)
	}

	var resp libp2pCandidacyResponse
	if err := json.NewDecoder(bufio.NewReader(s)).Decode(&resp); err != nil {
		return false, fmt.Errorf("decode candidacy resp: %w", err)
	}

	return resp.Accepted, nil
}

// libp2pBroadcastCandidacyToAll envia a candidatura para todos os peers conhecidos.
func libp2pBroadcastCandidacyToAll(ctx context.Context, h host.Host, registry *libp2pPeerRegistry, candidate ArtifactFetchCandidate) int {
	count := 0
	agentIDs := registry.AgentIDs()
	for _, agentID := range agentIDs {
		if strings.EqualFold(strings.TrimSpace(agentID), strings.TrimSpace(candidate.AgentID)) {
			continue
		}
		peerID, ok := registry.Lookup(agentID)
		if !ok {
			continue
		}
		accepted, err := libp2pBroadcastCandidacy(ctx, h, peerID, candidate)
		if err != nil {
			continue
		}
		if accepted {
			count++
		}
	}
	return count
}

// libp2pBroadcastFetchHeartbeat envia um heartbeat de fetch para um peer específico.
func libp2pBroadcastFetchHeartbeat(ctx context.Context, h host.Host, peerID peer.ID, hb ArtifactFetchHeartbeat) error {
	s, err := h.NewStream(ctx, peerID, protoFetchHeartbeat)
	if err != nil {
		return fmt.Errorf("stream heartbeat: %w", err)
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(libp2pStreamTimeout))

	if err := json.NewEncoder(s).Encode(hb); err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	return nil
}
