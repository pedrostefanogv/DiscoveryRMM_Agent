package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func (c *p2pCoordinator) DownloadArtifactFromPeer(ctx context.Context, artifactName, sourcePeerID string) (P2PArtifactView, error) {
	rawArtifactName := strings.TrimSpace(artifactName)
	artifactName = sanitizeArtifactName(artifactName)
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
	load := c.collectHostLoad()
	if !canServePartsNow(load) {
		err := fmt.Errorf("host sobrecarregado, recusando download de artifact")
		c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
		return P2PArtifactView{}, err
	}

	requesterID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
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
			err := fmt.Errorf("manifest indisponivel: %w", manifestErr)
			c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", false, err.Error())
			c.emitTransferDone(artifactName, sourcePeerID, "pull", err)
			return P2PArtifactView{}, err
		}

		destDir := c.app.p2pTempDir()
		sched := newP2PChunkScheduler()
		lp2pPeers := []libp2pPeer{{agentID: sourcePeerID, peerID: peerID}}
		path, totalBytes, err := downloadChunkedLibp2p(ctx, h, lp2pPeers, manifest, artifactName, requesterID, destDir, sched, c.dynamicMaxParallelChunks(),
			func(chunkIdx int, readSoFar, chunkSize int64, totalChunks int) {
				c.emitTransferProgress(p2pTransferProgress{
					ArtifactName: artifactName,
					PeerID:       sourcePeerID,
					BytesRead:    int64(chunkIdx)*chunkSize + readSoFar,
					TotalBytes:   int64(totalChunks) * chunkSize,
					Operation:    "pull",
					ChunkIndex:   chunkIdx,
					TotalChunks:  totalChunks,
				})
			})
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
func (c *p2pCoordinator) downloadArtifactSwarm(ctx context.Context, artifactName string) (P2PArtifactView, error) {
	rawArtifactName := strings.TrimSpace(artifactName)
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		err := fmt.Errorf("artifact inválido")
		c.appendAudit("swarm-pull", rawArtifactName, "", "automation", false, err.Error())
		return P2PArtifactView{}, err
	}

	// Guarda de sobrecarga: recusar servir se o host estiver pesado.
	if !canServePartsNow(c.collectHostLoad()) {
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

	requesterID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
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
		err := fmt.Errorf("manifest indisponivel: %w", manifestErr)
		c.appendAudit("swarm-pull", artifactName, peerEntries[0].peerID, "automation", false, err.Error())
		return P2PArtifactView{}, err
	}

	destDir := c.app.p2pTempDir()
	sched := newP2PChunkScheduler()
	lp2pPeers := make([]libp2pPeer, len(peerEntries))
	for i, pe := range peerEntries {
		lp2pPeers[i] = libp2pPeer{agentID: pe.peerID, peerID: pe.libp2pID}
	}
	path, totalBytes, err := downloadChunkedLibp2p(ctx, h, lp2pPeers, manifest, artifactName, requesterID, destDir, sched, c.dynamicMaxParallelChunks(),
		func(chunkIdx int, readSoFar, chunkSize int64, totalChunks int) {
			// Progresso agregado de todos os chunks
			c.emitTransferProgress(p2pTransferProgress{
				ArtifactName: artifactName,
				PeerID:       fmt.Sprintf("%d peers", len(peerEntries)),
				BytesRead:    int64(chunkIdx)*chunkSize + readSoFar,
				TotalBytes:   int64(totalChunks) * chunkSize,
				Operation:    "swarm-pull",
				ChunkIndex:   chunkIdx,
				TotalChunks:  totalChunks,
			})
		})
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
func (c *p2pCoordinator) DownloadArtifactByID(ctx context.Context, artifactID, sourcePeerID string) (P2PArtifactView, error) {
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
func (c *p2pCoordinator) buildArtifactView(artifactName, artifactID, path string) (P2PArtifactView, error) {
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
