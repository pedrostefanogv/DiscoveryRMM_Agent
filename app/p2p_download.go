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
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("artifact invalido")
	}
	p, err := c.findPeerByAgentID(sourcePeerID)
	if err != nil {
		return P2PArtifactView{}, err
	}

	requesterID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	if requesterID == "" {
		requesterID = "peer-local"
	}

	_ = p
	// Caminho libp2p: solicita acesso e faz download via streams.
	if h, registry := c.libp2pHostAndRegistry(); h != nil && registry != nil {
		peerID, ok := registry.Lookup(sourcePeerID)
		if !ok {
			return P2PArtifactView{}, fmt.Errorf("peer nao registrado no libp2p")
		}
		access, err := libp2pRequestAccess(ctx, h, peerID, artifactName, requesterID)
		if err != nil {
			return P2PArtifactView{}, err
		}
		path, size, err := libp2pDownloadArtifact(ctx, h, peerID, access, c.app.p2pTempDir())
		if err != nil {
			return P2PArtifactView{}, err
		}
		c.recordBytesDownloaded(size)
		c.appendAudit("pull", artifactName, sourcePeerID, "libp2p", true, "artifact baixado via libp2p")
		return c.buildArtifactView(artifactName, access.ArtifactID, path)
	}

	return P2PArtifactView{}, fmt.Errorf("libp2p indisponivel para download do artifact")
}

// downloadArtifactSwarm encontra todos os peers que possuem o artifact e faz
// download chunked quando ≥2 peers estão disponíveis, usando libp2p streams.
func (c *p2pCoordinator) downloadArtifactSwarm(ctx context.Context, artifactName string) (P2PArtifactView, error) {
	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactView{}, fmt.Errorf("artifact invalido")
	}

	avail := c.FindArtifactPeers(artifactName)
	if !avail.Found || len(avail.PeerAgentIDs) == 0 {
		return P2PArtifactView{}, fmt.Errorf("nenhum peer possui o artifact %q", artifactName)
	}

	requesterID := strings.TrimSpace(c.app.GetDebugConfig().AgentID)
	if requesterID == "" {
		requesterID = "peer-local"
	}

	h, registry := c.libp2pHostAndRegistry()
	cfg := c.app.GetP2PConfig()

	// Coletar tokens de acesso de todos os peers via libp2p.
	var accesses []P2PArtifactAccess
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
		acc, err := libp2pRequestAccess(ctx, h, lpID, artifactName, requesterID)
		if err != nil {
			continue
		}
		accesses = append(accesses, acc)
		peerEntries = append(peerEntries, peerEntry{peerID: pID, libp2pID: lpID, useLibp2p: true})
	}

	if len(accesses) == 0 {
		return P2PArtifactView{}, fmt.Errorf("nenhum peer retornou token de acesso para %q", artifactName)
	}

	// Peer único: download simples (sem manifest).
	if len(accesses) < 2 || cfg.ChunkSizeBytes == 0 {
		if len(peerEntries) == 0 || h == nil {
			return P2PArtifactView{}, fmt.Errorf("libp2p indisponivel para download do artifact")
		}
		path, size, err := libp2pDownloadArtifact(ctx, h, peerEntries[0].libp2pID, accesses[0], c.app.p2pTempDir())
		if err != nil {
			return P2PArtifactView{}, err
		}
		c.recordBytesDownloaded(size)
		return c.buildArtifactView(artifactName, accesses[0].ArtifactID, path)
	}

	// Multi-peer: buscar manifest e fazer download em chunks via libp2p.
	var manifest P2PChunkManifest
	if len(peerEntries) == 0 || h == nil {
		return P2PArtifactView{}, fmt.Errorf("libp2p indisponivel para manifest do artifact")
	}
	var manifestErr error
	manifest, manifestErr = libp2pFetchManifest(ctx, h, peerEntries[0].libp2pID, artifactName, requesterID)
	if manifestErr != nil || manifest.TotalChunks == 0 {
		return P2PArtifactView{}, fmt.Errorf("manifest indisponivel: %w", manifestErr)
	}

	destDir := c.app.p2pTempDir()
	sched := newP2PChunkScheduler()
	lp2pPeers := make([]libp2pPeer, len(peerEntries))
	for i, pe := range peerEntries {
		lp2pPeers[i] = libp2pPeer{agentID: pe.peerID, peerID: pe.libp2pID}
	}
	path, totalBytes, err := downloadChunkedLibp2p(ctx, h, lp2pPeers, manifest, artifactName, requesterID, destDir, sched)
	if err != nil {
		c.appendAudit("swarm-pull", artifactName, "", "automation", false, err.Error())
		return P2PArtifactView{}, err
	}
	c.recordBytesDownloaded(totalBytes)
	c.recordChunkedDownload(manifest.TotalChunks)
	c.appendAudit("swarm-pull", artifactName, fmt.Sprintf("%d peers", len(accesses)),
		"automation", true, fmt.Sprintf("download em %d chunks de %d peers", manifest.TotalChunks, len(accesses)))

	artifactID := CanonicalArtifactID(manifest.ArtifactID, artifactName, "")
	return c.buildArtifactView(artifactName, artifactID, path)
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
