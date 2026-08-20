package p2p

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func (c *Coordinator) DownloadArtifactFromPeer(ctx context.Context, artifactName, sourcePeerID string) (P2PArtifactView, error) {
	rawArtifactName := strings.TrimSpace(artifactName)
	artifactName = SanitizeArtifactName(artifactName)
	if artifactName == "" {
		err := fmt.Errorf("artifact inválido")
		c.appendAudit("pull", rawArtifactName, sourcePeerID, "libp2p", false, err.Error())
		return P2PArtifactView{}, err
	}
	if _, err := c.findPeerByAgentID(sourcePeerID); err != nil {
		c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
		return P2PArtifactView{}, err
	}

	// Guarda de sobrecarga: recusar servir se o host estiver pesado.
	load := c.CollectHostLoad()
	if !canServePartsNow(load) {
		err := fmt.Errorf("host sobrecarregado, recusando download de artifact")
		c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
		return P2PArtifactView{}, err
	}

	requesterID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	if requesterID == "" {
		requesterID = "peer-local"
	}

	// Caminho libp2p: sempre chunked para resiliência e resume.
	if h, registry := c.libp2pHostAndRegistry(); h != nil && registry != nil {
		peerID, ok := registry.Lookup(sourcePeerID)
		if !ok {
			err := fmt.Errorf("peer não registrado no libp2p")
			c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
			return P2PArtifactView{}, err
		}

		// Obter manifest do peer para download chunked.
		manifest, manifestErr := libp2pFetchManifest(ctx, h, peerID, artifactName, requesterID)
		if manifestErr != nil || manifest.TotalChunks == 0 {
			// Cache stale: peer não tem mais este artifact. Invalida.
			c.InvalidatePeerArtifact(sourcePeerID, artifactName)
			err := fmt.Errorf("manifest indisponivel: %w", manifestErr)
			c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
			c.emitTransferDone(artifactName, sourcePeerID, "pull", err)
			return P2PArtifactView{}, err
		}
		// Valida a consistência interna do manifest ANTES do download. Isso
		// detecta manifests stale (de um artifact republicado) que causariam
		// checksum divergente nos chunks.
		if vErr := validateDownloadManifest(&manifest); vErr != nil {
			err := fmt.Errorf("manifest rejeitado: %w", vErr)
			c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
			c.emitTransferDone(artifactName, sourcePeerID, "pull", err)
			return P2PArtifactView{}, err
		}

		destDir := c.deps.P2PTempDir()
		sched := newP2PChunkScheduler()
		lp2pPeers := []libp2pPeer{{agentID: sourcePeerID, peerID: peerID}}
		// Serializa downloads do mesmo artifact para não corromper o partsDir.
		unlock := c.lockDownload(artifactName)
		// Rastreia os índices dos chunks concluídos para progresso preciso
		// (chunks completam fora de ordem em paralelo).
		completedIdx := make(map[int]bool)
		path, totalBytes, err := downloadChunkedLibp2p(ctx, h, lp2pPeers, manifest, artifactName, requesterID, destDir, sched, c.dynamicMaxParallelChunks(),
			nil, // onChunkProgress: desabilitado; usamos onChunkComplete para progresso monotônico
			func(chunkIdx, completed, total int) {
				completedIdx[chunkIdx] = true
				c.emitTransferProgress(p2pTransferProgress{
					ArtifactName:    artifactName,
					PeerID:          sourcePeerID,
					BytesRead:       completedChunksBytes(manifest, completedIdx),
					TotalBytes:      manifest.TotalSize,
					Operation:       "pull",
					CompletedChunks: completed,
					TotalChunks:     total,
				})
			},
			func(phase string) {
				c.emitTransferProgress(p2pTransferProgress{
					ArtifactName:    artifactName,
					PeerID:          sourcePeerID,
					TotalBytes:      manifest.TotalSize,
					TotalChunks:     manifest.TotalChunks,
					CompletedChunks: manifest.TotalChunks,
					BytesRead:       manifest.TotalSize,
					Operation:       "pull",
					Phase:           phase,
				})
			},
			func(msg string) {
				c.deps.Log(msg)
			})
		unlock()
		if err != nil {
			c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
			c.emitTransferDone(artifactName, sourcePeerID, "pull", err)
			return P2PArtifactView{}, err
		}
		c.recordBytesDownloaded(totalBytes)
		c.recordChunkedDownload(manifest.TotalChunks)
		c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", true,
			fmt.Sprintf("artifact baixado via libp2p em %d chunks", manifest.TotalChunks))
		c.emitTransferDone(artifactName, sourcePeerID, "pull", nil)
		// Pipeline pós-download: validar checksum + cachear manifest
		go c.finalizeDownloadedArtifact(artifactName, path, manifest.SHA256)
		go c.updateManifestCacheAfterDownload(artifactName, path)
		return c.buildArtifactView(artifactName, manifest.ArtifactID, path)
	}

	err := fmt.Errorf("libp2p indisponível para download do artifact")
	c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
	return P2PArtifactView{}, err
}

// downloadArtifactSwarm encontra todos os peers que possuem o artifact e faz
// download chunked via libp2p streams, mesmo com peer único (resiliência/resume).
func (c *Coordinator) DownloadArtifactSwarm(ctx context.Context, artifactName string) (P2PArtifactView, error) {
	rawArtifactName := strings.TrimSpace(artifactName)
	artifactName = SanitizeArtifactName(artifactName)
	if artifactName == "" {
		err := fmt.Errorf("artifact inválido")
		c.appendAudit("swarm-pull", rawArtifactName, "", "automation", false, err.Error())
		return P2PArtifactView{}, err
	}

	// Guarda de sobrecarga: recusar servir se o host estiver pesado.
	if !canServePartsNow(c.CollectHostLoad()) {
		err := fmt.Errorf("host sobrecarregado, recusando swarm pull")
		c.appendAudit("swarm-pull", artifactName, "", "automation", false, err.Error())
		return P2PArtifactView{}, err
	}

	avail := c.FindArtifactPeers(artifactName)
	if !avail.Found || len(avail.PeerAgentIDs) == 0 {
		err := fmt.Errorf("nenhum peer possui o artifact %q", artifactName)
		c.appendAudit("swarm-pull", artifactName, "", "automation", false, err.Error())
		return P2PArtifactView{}, err
	}

	requesterID := strings.TrimSpace(c.deps.GetDebugConfig().AgentID)
	if requesterID == "" {
		requesterID = "peer-local"
	}

	h, registry := c.libp2pHostAndRegistry()

	// Coletar peer entries (agentID + libp2p ID) para chunked download.
	type peerEntry struct {
		peerID    string
		libp2pID  peer.ID
		useLibp2p bool
	}
	var peerEntries []peerEntry

	for _, pID := range avail.PeerAgentIDs {
		if h == nil || registry == nil {
			continue
		}
		lpID, ok := registry.Lookup(pID)
		if !ok {
			continue
		}
		peerEntries = append(peerEntries, peerEntry{peerID: pID, libp2pID: lpID, useLibp2p: true})
	}

	if len(peerEntries) == 0 {
		err := fmt.Errorf("nenhum peer resolvido via libp2p para %q", artifactName)
		c.appendAudit("swarm-pull", artifactName, "", "automation", false, err.Error())
		return P2PArtifactView{}, err
	}

	// Sempre chunked: buscar manifest e fazer download em chunks via libp2p.
	var manifest P2PChunkManifest
	if h == nil {
		err := fmt.Errorf("libp2p indisponível para manifest do artifact")
		c.appendAudit("swarm-pull", artifactName, "", "automation", false, err.Error())
		return P2PArtifactView{}, err
	}
	var manifestErr error
	manifest, manifestErr = libp2pFetchManifest(ctx, h, peerEntries[0].libp2pID, artifactName, requesterID)
	if manifestErr != nil || manifest.TotalChunks == 0 {
		// Cache stale: primeiro peer não tem mais este artifact. Invalida e tenta próximo.
		c.InvalidatePeerArtifact(peerEntries[0].peerID, artifactName)
		// Tenta o próximo peer da lista, se houver.
		if len(peerEntries) > 1 {
			manifest, manifestErr = libp2pFetchManifest(ctx, h, peerEntries[1].libp2pID, artifactName, requesterID)
		}
		if manifestErr != nil || manifest.TotalChunks == 0 {
			err := fmt.Errorf("manifest indisponivel: %w", manifestErr)
			c.appendAudit("swarm-pull", artifactName, peerEntries[0].peerID, "automation", false, err.Error())
			return P2PArtifactView{}, err
		}
	}

	// Valida a consistência interna do manifest ANTES do download. Isso
	// detecta manifests stale (de um artifact republicado) que causariam
	// checksum divergente nos chunks.
	if vErr := validateDownloadManifest(&manifest); vErr != nil {
		err := fmt.Errorf("manifest rejeitado: %w", vErr)
		c.appendAudit("swarm-pull", artifactName, peerEntries[0].peerID, "automation", false, err.Error())
		c.emitTransferDone(artifactName, peerEntries[0].peerID, "swarm-pull", err)
		return P2PArtifactView{}, err
	}

	// Validação cross-peer: verifica que os demais peers anunciam a mesma
	// versão do artifact (mesmo SHA256 e tamanho). Peers com versão divergente
	// são removidos do swarm para evitar montar um arquivo corrompido com
	// chunks de versões diferentes.
	if len(peerEntries) > 1 {
		validPeers := make([]peerEntry, 0, len(peerEntries))
		validPeers = append(validPeers, peerEntries[0])
		for _, pe := range peerEntries[1:] {
			pm, pmErr := libp2pFetchManifest(ctx, h, pe.libp2pID, artifactName, requesterID)
			if pmErr != nil || pm.TotalChunks == 0 {
				c.InvalidatePeerArtifact(pe.peerID, artifactName)
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(pm.SHA256), strings.TrimSpace(manifest.SHA256)) ||
				pm.TotalSize != manifest.TotalSize {
				c.deps.Log(fmt.Sprintf("[p2p][swarm] peer %s com versao divergente do artifact %s (sha=%s size=%d), removido do swarm",
					pe.peerID, artifactName, pm.SHA256, pm.TotalSize))
				continue
			}
			validPeers = append(validPeers, pe)
		}
		peerEntries = validPeers
		if len(peerEntries) == 0 {
			err := fmt.Errorf("nenhum peer com versao consistente do artifact %q", artifactName)
			c.appendAudit("swarm-pull", artifactName, "", "automation", false, err.Error())
			return P2PArtifactView{}, err
		}
	}

	destDir := c.deps.P2PTempDir()
	sched := newP2PChunkScheduler()
	lp2pPeers := make([]libp2pPeer, len(peerEntries))
	for i, pe := range peerEntries {
		lp2pPeers[i] = libp2pPeer{agentID: pe.peerID, peerID: pe.libp2pID}
	}
	// Serializa downloads do mesmo artifact para não corromper o partsDir.
	unlock := c.lockDownload(artifactName)
	// Rastreia os índices dos chunks concluídos para progresso preciso
	// (chunks completam fora de ordem em paralelo).
	completedIdx := make(map[int]bool)
	path, totalBytes, err := downloadChunkedLibp2p(ctx, h, lp2pPeers, manifest, artifactName, requesterID, destDir, sched, c.dynamicMaxParallelChunks(),
		nil, // onChunkProgress: desabilitado; usamos onChunkComplete
		func(chunkIdx, completed, total int) {
			completedIdx[chunkIdx] = true
			// Calcula bytes reais somando o tamanho de cada chunk concluído.
			// O último chunk geralmente é menor que manifest.ChunkSize, então
			// usar completed * manifest.ChunkSize faria a barra "pular" no final.
			bytesRead := completedChunksBytes(manifest, completedIdx)
			c.emitTransferProgress(p2pTransferProgress{
				ArtifactName:    artifactName,
				PeerID:          fmt.Sprintf("%d peers", len(peerEntries)),
				BytesRead:       bytesRead,
				TotalBytes:      manifest.TotalSize,
				Operation:       "swarm-pull",
				CompletedChunks: completed,
				TotalChunks:     total,
			})
		},
		func(phase string) {
			c.emitTransferProgress(p2pTransferProgress{
				ArtifactName:    artifactName,
				PeerID:          fmt.Sprintf("%d peers", len(peerEntries)),
				TotalBytes:      manifest.TotalSize,
				TotalChunks:     manifest.TotalChunks,
				CompletedChunks: manifest.TotalChunks,
				BytesRead:       manifest.TotalSize,
				Operation:       "swarm-pull",
				Phase:           phase,
			})
		},
		func(msg string) {
			c.deps.Log(msg)
		})
	unlock()
	if err != nil {
		c.appendAudit("swarm-pull", artifactName, "", "automation", false, err.Error())
		c.emitTransferDone(artifactName, fmt.Sprintf("%d peers", len(peerEntries)), "swarm-pull", err)
		return P2PArtifactView{}, err
	}
	c.recordBytesDownloaded(totalBytes)
	c.recordChunkedDownload(manifest.TotalChunks)
	c.appendAudit("swarm-pull", artifactName, fmt.Sprintf("%d peers", len(peerEntries)),
		"automation", true, fmt.Sprintf("download em %d chunks de %d peers", manifest.TotalChunks, len(peerEntries)))
	c.emitTransferDone(artifactName, fmt.Sprintf("%d peers", len(peerEntries)), "swarm-pull", nil)

	artifactID := CanonicalArtifactID(manifest.ArtifactID, artifactName, "")
	// Pipeline pós-download: validar checksum + cachear manifest
	go c.finalizeDownloadedArtifact(artifactName, path, manifest.SHA256)
	go c.updateManifestCacheAfterDownload(artifactName, path)
	return c.buildArtifactView(artifactName, artifactID, path)
}

// DownloadArtifactByID faz download de um artifact via libp2p usando o artifactID
// (GUID de release ou "sha256:<hex>") e o peerID de origem.
// Diferente de DownloadArtifactFromPeer, aceita um artifactID explícito e
// resolve o nome do arquivo a partir do índice de artifacts do peer.
func (c *Coordinator) DownloadArtifactByID(ctx context.Context, artifactID, sourcePeerID string) (P2PArtifactView, error) {
	artifactID = strings.TrimSpace(artifactID)
	sourcePeerID = strings.TrimSpace(sourcePeerID)
	if artifactID == "" {
		return P2PArtifactView{}, fmt.Errorf("artifactID invalido")
	}
	if sourcePeerID == "" {
		return P2PArtifactView{}, fmt.Errorf("sourcePeerID invalido")
	}

	// Resolve o artifactName a partir do índice de artifacts do peer
	var artifactName string
	for _, peer := range c.GetPeerArtifactIndex() {
		if !strings.EqualFold(strings.TrimSpace(peer.PeerAgentID), sourcePeerID) {
			continue
		}
		for _, art := range peer.Artifacts {
			if strings.EqualFold(strings.TrimSpace(art.ArtifactID), artifactID) {
				artifactName = strings.TrimSpace(art.ArtifactName)
				break
			}
		}
		if artifactName != "" {
			break
		}
	}
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("artifact nao encontrado no indice do peer %s para artifactID=%s", sourcePeerID, artifactID)
	}

	return c.DownloadArtifactFromPeer(ctx, artifactName, sourcePeerID)
}

// buildArtifactView reads stat + checksum for a file and returns a P2PArtifactView.
func (c *Coordinator) buildArtifactView(artifactName, artifactID, path string) (P2PArtifactView, error) {
	info, err := os.Stat(path)
	if err != nil {
		return P2PArtifactView{}, err
	}
	checksum, err := computeFileSHA256(path)
	if err != nil {
		return P2PArtifactView{}, err
	}
	return P2PArtifactView{
		ArtifactID:       CanonicalArtifactID(artifactID, artifactName, ""),
		ArtifactName:     artifactName,
		SizeBytes:        info.Size(),
		ModifiedAtUTC:    formatTimeRFC3339(info.ModTime()),
		ChecksumSHA256:   checksum,
		Available:        true,
		LastHeartbeatUTC: formatTimeRFC3339(time.Now().UTC()),
	}, nil
}
