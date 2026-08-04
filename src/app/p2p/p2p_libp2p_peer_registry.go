package p2p

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/peer"
)

// verifySHA256 verifica se data bate com o hex SHA256 esperado.
func verifySHA256(data []byte, expected string) bool {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	return strings.EqualFold(got, strings.TrimSpace(expected))
}

// ── Mapa agentID → libp2p peer.ID ────────────────────────────────────────────

// libp2pPeerRegistry mantém mapeamento agentID → peer.ID libp2p para lookup
// durante operações de transferência. Atualizado pelo notifee quando um peer é
// conectado.
// mu protege peers contra acesso concorrente de goroutines de stream handlers
// (inbound/outbound) e leituras do coordinator.
type libp2pPeerRegistry struct {
	mu    sync.RWMutex
	peers map[string]peer.ID
}

func newLibp2pPeerRegistry() *libp2pPeerRegistry {
	return &libp2pPeerRegistry{peers: make(map[string]peer.ID)}
}

// Register associa um agentID a um peer.ID libp2p. Seguro para uso concorrente.
func (r *libp2pPeerRegistry) Register(agentID string, id peer.ID) {
	if r == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(agentID))
	if key == "" {
		return
	}
	r.mu.Lock()
	r.peers[key] = id
	r.mu.Unlock()
}

// RegisterStrict accepts the first mapping for an agentID and rejects
// conflicting peer IDs for the same agentID.
func (r *libp2pPeerRegistry) RegisterStrict(agentID string, id peer.ID) (accepted bool, existing peer.ID, conflict bool) {
	if r == nil {
		return false, "", false
	}
	key := strings.ToLower(strings.TrimSpace(agentID))
	if key == "" {
		return false, "", false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if prev, ok := r.peers[key]; ok {
		if prev != "" && id != "" && prev != id {
			return false, prev, true
		}
		return true, prev, false
	}

	r.peers[key] = id
	return true, "", false
}

// Lookup retorna o peer.ID para um agentID, se registrado. Seguro para uso concorrente.
func (r *libp2pPeerRegistry) Lookup(agentID string) (peer.ID, bool) {
	if r == nil {
		return "", false
	}
	key := strings.ToLower(strings.TrimSpace(agentID))
	r.mu.RLock()
	id, ok := r.peers[key]
	r.mu.RUnlock()
	return id, ok
}

// AgentIDs retorna todos os agentIDs registrados. Seguro para uso concorrente.
func (r *libp2pPeerRegistry) AgentIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	ids := make([]string, 0, len(r.peers))
	for id := range r.peers {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	return ids
}
