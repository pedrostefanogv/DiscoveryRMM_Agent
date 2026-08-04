package p2p

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const p2pPeerCacheMaxEntries = 128

// p2pCachedPeer representa um peer conhecido persistido localmente entre reinicializações.
type p2pCachedPeer struct {
	PeerID     string    `json:"peerId"`
	AgentID    string    `json:"agentId,omitempty"`
	Addrs      []string  `json:"addrs"`
	Port       int       `json:"port"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	// FailCount acumula falhas consecutivas de conexão. Após atingir
	// p2pCachedPeerMaxFailures, o peer é removido do cache para evitar
	// tentativas infinitas em peers permanentemente inalcançáveis.
	FailCount int `json:"failCount,omitempty"`
}

// p2pCachedPeerMaxFailures é o número máximo de falhas consecutivas antes
// de remover um peer do cache. 3 falhas toleram flutuações temporárias de
// rede sem descartar peers que voltam a responder.
const p2pCachedPeerMaxFailures = 3

func (c *Coordinator) p2pPeerCachePath() string {
	return filepath.Join(c.deps.GetDataDir(), "p2p_peer_cache.json")
}

// loadP2PPeerCache lê o cache do disco. Retorna slice vazio se o arquivo não existir.
func (c *Coordinator) loadP2PPeerCache() ([]p2pCachedPeer, error) {
	data, err := os.ReadFile(c.p2pPeerCachePath())
	if os.IsNotExist(err) {
		return []p2pCachedPeer{}, nil
	}
	if err != nil {
		return nil, err
	}
	var peers []p2pCachedPeer
	if err := json.Unmarshal(data, &peers); err != nil {
		// Arquivo corrompido — ignora e começa do zero.
		return []p2pCachedPeer{}, nil
	}
	return peers, nil
}

// saveP2PPeerCache persiste o cache atomicamente (write tmp + rename).
func (c *Coordinator) saveP2PPeerCache(peers []p2pCachedPeer) error {
	if err := os.MkdirAll(filepath.Dir(c.p2pPeerCachePath()), 0o755); err != nil {
		return err
	}
	// Limitar tamanho máximo do cache.
	if len(peers) > p2pPeerCacheMaxEntries {
		peers = peers[len(peers)-p2pPeerCacheMaxEntries:]
	}
	data, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.p2pPeerCachePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.p2pPeerCachePath())
}

// upsertP2PPeerCacheEntry insere ou atualiza uma entrada por PeerID.
func upsertP2PPeerCacheEntry(peers []p2pCachedPeer, entry p2pCachedPeer) []p2pCachedPeer {
	entry.PeerID = strings.TrimSpace(entry.PeerID)
	if entry.PeerID == "" {
		return peers
	}
	for i, p := range peers {
		if p.PeerID == entry.PeerID {
			peers[i] = entry
			return peers
		}
	}
	return append(peers, entry)
}

// removeP2PPeerCacheEntry remove todas as entradas com o PeerID dado.
func removeP2PPeerCacheEntry(peers []p2pCachedPeer, peerID string) []p2pCachedPeer {
	peerID = strings.TrimSpace(peerID)
	out := peers[:0]
	for _, p := range peers {
		if p.PeerID != peerID {
			out = append(out, p)
		}
	}
	return out
}
