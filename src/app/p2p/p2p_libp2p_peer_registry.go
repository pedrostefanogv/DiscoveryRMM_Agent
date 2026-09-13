package p2p

import (
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// verifySHA256Hex verifica se um digest SHA256 (já calculado, em bytes)
// bate com o hex SHA256 esperado.
//
// IMPORTANTE: o caller em libp2pDownloadChunk passa hasher.Sum(nil) — o digest
// já calculado do chunk. A versão anterior fazia sha256.Sum256(data), ou seja,
// um DOUBLE-HASH (sha256 do sha256), fazendo 100% dos chunks falharem com
// "checksum divergente" mesmo com dados íntegros (esperado == obtido no log).
func verifySHA256Hex(digest []byte, expected string) bool {
	got := hex.EncodeToString(digest)
	return strings.EqualFold(got, strings.TrimSpace(expected))
}

// ── Mapa agentID → libp2p peer.ID ────────────────────────────────────────────

// registryConflictWindow é a janela em que um peer.ID divergente para o mesmo
// agentID é tratado como conflito suspeito (identidade trocada em produção
// ativa). Registro após a janela (peer ausente por tempo prolongado —
// máquina formatada/rede indisponível) substitui a entrada sem conflito.
const registryConflictWindow = 10 * time.Minute

// registryStalePrune é o tempo sem contato para remover entradas órfãs
// (lazy prune a cada Register).
const registryStalePrune = 24 * time.Hour

// libp2pPeerRegistry mantém mapeamento agentID → peer.ID libp2p para lookup
// durante operações de transferência. Atualizado pelo notifee quando um peer é
// conectado.
// mu protege peers/seen contra acesso concorrente de goroutines de stream
// handlers (inbound/outbound) e leituras do coordinator.
//
// A5: o registro guarda lastSeen por entrada. Conflito de identidade só é
// sinalizado para registros RECENTES (< registryConflictWindow); registros
// stale são substituídos sem conflito — reinício do agente remoto (identidade
// efêmera legada ou chave regenerada) não deve mais bloquear a malha.
type libp2pPeerRegistry struct {
	mu    sync.RWMutex
	peers map[string]peer.ID
	seen  map[string]time.Time // último contato/registro por agentID
}

func newLibp2pPeerRegistry() *libp2pPeerRegistry {
	return &libp2pPeerRegistry{
		peers: make(map[string]peer.ID),
		seen:  make(map[string]time.Time),
	}
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
	r.seen[key] = time.Now()
	r.pruneStaleLocked()
	r.mu.Unlock()
}

// RegisterStrict accepts the first mapping for an agentID and rejects
// conflicting peer IDs for the same agentID — mas somente enquanto o registro
// anterior estiver "fresco" (visto em registryConflictWindow). Registros
// stale são substituídos silenciosamente: rotação legítima de identidade,
// não spoofing (A5).
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

	now := time.Now()
	if prev, ok := r.peers[key]; ok {
		if prev != "" && id != "" && prev != id {
			if lastSeen, seen := r.seen[key]; seen && now.Sub(lastSeen) < registryConflictWindow {
				return false, prev, true
			}
			// Entrada stale — substitui e NÃO bloqueia.
			r.peers[key] = id
			r.seen[key] = now
			return true, prev, false
		}
		r.seen[key] = now
		return true, prev, false
	}

	r.peers[key] = id
	r.seen[key] = now
	return true, "", false
}

// pruneStaleLocked remove entradas sem contato recente. Caller segura r.mu.
func (r *libp2pPeerRegistry) pruneStaleLocked() {
	now := time.Now()
	for key, last := range r.seen {
		if now.Sub(last) > registryStalePrune {
			delete(r.seen, key)
			delete(r.peers, key)
		}
	}
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
