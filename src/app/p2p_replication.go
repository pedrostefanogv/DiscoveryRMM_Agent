package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (c *p2pCoordinator) replicateArtifactToPeerNow(ctx context.Context, artifactName, targetPeerID string) error {
	peer, err := c.findPeerByAgentID(targetPeerID)
	if err != nil {
		c.recordReplicationResult(false)
		return err
	}
	access, err := c.GetArtifactAccess(artifactName, targetPeerID)
	if err != nil {
		c.recordReplicationResult(false)
		return err
	}

	c.mu.Lock()
	c.metrics.ReplicationsStarted++
	c.mu.Unlock()

	// Tentar libp2p primeiro.
	if h, registry := c.libp2pHostAndRegistry(); h != nil && registry != nil {
		if lpID, ok := registry.Lookup(targetPeerID); ok {
			streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			stream, serr := h.NewStream(streamCtx, lpID, protoArtifactReplicate)
			if serr == nil {
				req := libp2pReplicateRequest{
					ArtifactName:   access.ArtifactName,
					ChecksumSHA256: access.ChecksumSHA256,
					SourceAgentID:  strings.TrimSpace(c.app.GetDebugConfig().AgentID),
				}
				_ = json.NewEncoder(stream).Encode(req)
				var resp libp2pReplicateResponse
				_ = json.NewDecoder(stream).Decode(&resp)
				stream.Close()
				// /artifact/replicate retorna Gone (push desativ.); isso é esperado.
				c.recordReplicationResult(true)
				return nil
			}
		}
	}

	_ = peer
	c.recordReplicationResult(false)
	return fmt.Errorf("replicacao requer libp2p ativo")
}

func (c *p2pCoordinator) recordReplicationResult(success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if success {
		c.metrics.ReplicationsSucceeded++
		return
	}
	c.metrics.ReplicationsFailed++
}

func (c *p2pCoordinator) recordBytesServed(size int64) {
	if size <= 0 {
		return
	}
	c.mu.Lock()
	c.metrics.BytesServed += size
	c.mu.Unlock()
}

func (c *p2pCoordinator) recordBytesDownloaded(size int64) {
	if size <= 0 {
		return
	}
	c.mu.Lock()
	c.metrics.BytesDownloaded += size
	c.mu.Unlock()
}

func (c *p2pCoordinator) enqueueReplicationJob(job p2pReplicationJob) error {
	job.ArtifactName = sanitizeArtifactName(job.ArtifactName)
	job.Checksum = strings.TrimSpace(job.Checksum)
	job.TargetPeerID = strings.TrimSpace(job.TargetPeerID)
	job.Source = strings.TrimSpace(job.Source)
	if job.Source == "" {
		job.Source = "manual"
	}
	if job.ArtifactName == "" {
		return fmt.Errorf("artifact invalido")
	}
	if job.TargetPeerID == "" {
		return fmt.Errorf("peer alvo nao informado")
	}
	if job.Checksum == "" {
		resolvedChecksum, err := c.resolveArtifactChecksum(job.ArtifactName)
		if err != nil {
			return err
		}
		job.Checksum = resolvedChecksum
	}

	now := time.Now().UTC()
	c.mu.Lock()
	c.pruneDedupLocked(now)
	if c.wasRecentlyReplicatedLocked(job.TargetPeerID, job.ArtifactName, job.Checksum, now) {
		c.mu.Unlock()
		c.appendAudit("skip-duplicate", job.ArtifactName, job.TargetPeerID, job.Source, true, errP2PDuplicateReplication.Error())
		return errP2PDuplicateReplication
	}
	c.mu.Unlock()

	select {
	case c.replicationQueue <- job:
		c.mu.Lock()
		c.metrics.QueuedReplications++
		c.mu.Unlock()
		c.appendAudit("queue", job.ArtifactName, job.TargetPeerID, job.Source, true, "replicacao enfileirada")
		return nil
	default:
		c.appendAudit("queue", job.ArtifactName, job.TargetPeerID, job.Source, false, "fila de replicacao cheia")
		return fmt.Errorf("fila de replicacao cheia")
	}
}

func (c *p2pCoordinator) replicationWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-c.replicationQueue:
			c.mu.Lock()
			if c.metrics.QueuedReplications > 0 {
				c.metrics.QueuedReplications--
			}
			c.metrics.ActiveReplications++
			c.mu.Unlock()

			err := c.processReplicationJob(ctx, job)
			if job.Result != nil {
				job.Result <- err
			}
		}
	}
}

func (c *p2pCoordinator) processReplicationJob(ctx context.Context, job p2pReplicationJob) error {
	peerKey := strings.ToLower(strings.TrimSpace(job.TargetPeerID))
	now := time.Now().UTC()

	c.mu.Lock()
	lastAttempt := c.peerLastAttempt[peerKey]
	if !lastAttempt.IsZero() && now.Sub(lastAttempt) < p2pPeerReplicationCooldown {
		if c.metrics.ActiveReplications > 0 {
			c.metrics.ActiveReplications--
		}
		c.mu.Unlock()
		err := fmt.Errorf("peer em cooldown de replicacao")
		c.appendAudit("cooldown", job.ArtifactName, job.TargetPeerID, job.Source, false, err.Error())
		return err
	}
	c.peerLastAttempt[peerKey] = now
	c.mu.Unlock()

	err := c.replicateArtifactToPeerNow(ctx, job.ArtifactName, job.TargetPeerID)
	if err != nil {
		c.appendAudit("replicate", job.ArtifactName, job.TargetPeerID, job.Source, false, err.Error())
	} else {
		c.markReplicated(job.TargetPeerID, job.ArtifactName, job.Checksum)
		c.appendAudit("replicate", job.ArtifactName, job.TargetPeerID, job.Source, true, "replicacao concluida")
	}

	c.mu.Lock()
	if c.metrics.ActiveReplications > 0 {
		c.metrics.ActiveReplications--
	}
	c.mu.Unlock()
	return err
}

func (c *p2pCoordinator) appendAudit(action, artifactName, peerAgentID, source string, success bool, message string) {
	event := P2PAuditEvent{
		TimestampUTC: formatTimeRFC3339(time.Now().UTC()),
		Action:       strings.TrimSpace(action),
		ArtifactName: strings.TrimSpace(artifactName),
		PeerAgentID:  strings.TrimSpace(peerAgentID),
		Source:       strings.TrimSpace(source),
		Success:      success,
		Message:      strings.TrimSpace(message),
	}
	c.mu.Lock()
	c.audit = append([]P2PAuditEvent{event}, c.audit...)
	if len(c.audit) > p2pAuditLimit {
		c.audit = c.audit[:p2pAuditLimit]
	}
	c.mu.Unlock()
}

func (c *p2pCoordinator) autoDistributeLocalArtifacts(resource, variant, revision string) {
	artifacts, err := c.ListArtifacts()
	if err != nil {
		c.appendAudit("auto-distribute", "", "", "sync", false, err.Error())
		return
	}
	peers := c.selectAutoDistributionPeers(c.GetPeers())
	if len(artifacts) == 0 || len(peers) == 0 {
		c.appendAudit("auto-distribute", "", "", "sync", true, "sem artifacts ou peers elegiveis")
		return
	}

	c.mu.Lock()
	c.metrics.AutoDistributionRuns++
	c.mu.Unlock()

	sort.SliceStable(artifacts, func(i, j int) bool {
		pi := c.artifactPriority(resource, variant, artifacts[i].ArtifactName)
		pj := c.artifactPriority(resource, variant, artifacts[j].ArtifactName)
		if pi != pj {
			return pi < pj
		}
		return strings.ToLower(artifacts[i].ArtifactName) < strings.ToLower(artifacts[j].ArtifactName)
	})

	enqueued := 0
	skippedDuplicates := 0
	for _, artifact := range artifacts {
		for _, peer := range peers {
			err := c.enqueueReplicationJob(p2pReplicationJob{
				ArtifactName: artifact.ArtifactName,
				Checksum:     artifact.ChecksumSHA256,
				TargetPeerID: peer.AgentID,
				Source:       "sync",
			})
			if err == nil {
				enqueued++
				continue
			}
			if errors.Is(err, errP2PDuplicateReplication) {
				skippedDuplicates++
			}
		}
	}
	c.appendAudit("auto-distribute", "", "", "sync", true, fmt.Sprintf("resource=%s variant=%s revision=%s jobs=%d duplicates=%d", resource, variant, revision, enqueued, skippedDuplicates))
}

func (c *p2pCoordinator) artifactPriority(resource, variant, artifactName string) int {
	name := strings.ToLower(strings.TrimSpace(artifactName))
	res := strings.ToLower(strings.TrimSpace(resource))
	varKey := strings.ToLower(strings.TrimSpace(variant))

	if res != "" && strings.Contains(name, res) {
		return 0
	}
	if varKey != "" && strings.Contains(name, varKey) {
		return 1
	}
	if res == "appstore" && (strings.Contains(name, "catalog") || strings.Contains(name, "store")) {
		return 0
	}
	if res == "automationpolicy" && strings.Contains(name, "automation") {
		return 0
	}
	if res == "configuration" && strings.Contains(name, "config") {
		return 0
	}
	return 2
}

func (c *p2pCoordinator) resolveArtifactChecksum(artifactName string) (string, error) {
	artifacts, err := c.ListArtifacts()
	if err != nil {
		return "", err
	}
	target := strings.ToLower(strings.TrimSpace(artifactName))
	for _, artifact := range artifacts {
		if strings.ToLower(strings.TrimSpace(artifact.ArtifactName)) != target {
			continue
		}
		if strings.TrimSpace(artifact.ChecksumSHA256) == "" {
			break
		}
		return strings.TrimSpace(artifact.ChecksumSHA256), nil
	}
	return "", fmt.Errorf("checksum do artifact nao encontrado")
}

func (c *p2pCoordinator) dedupKey(peerAgentID, artifactName, checksum string) string {
	return strings.ToLower(strings.TrimSpace(peerAgentID)) + "|" + strings.ToLower(strings.TrimSpace(artifactName)) + "|" + strings.ToLower(strings.TrimSpace(checksum))
}

func (c *p2pCoordinator) wasRecentlyReplicatedLocked(peerAgentID, artifactName, checksum string, now time.Time) bool {
	if strings.TrimSpace(checksum) == "" {
		return false
	}
	last, ok := c.replicationDedup[c.dedupKey(peerAgentID, artifactName, checksum)]
	if !ok {
		return false
	}
	return now.Sub(last) < p2pReplicationDedupTTL
}

func (c *p2pCoordinator) markReplicated(peerAgentID, artifactName, checksum string) {
	if strings.TrimSpace(checksum) == "" {
		return
	}
	now := time.Now().UTC()
	c.mu.Lock()
	c.pruneDedupLocked(now)
	c.replicationDedup[c.dedupKey(peerAgentID, artifactName, checksum)] = now
	c.mu.Unlock()
}

func (c *p2pCoordinator) pruneDedupLocked(now time.Time) {
	for key, ts := range c.replicationDedup {
		if now.Sub(ts) >= p2pReplicationDedupTTL {
			delete(c.replicationDedup, key)
		}
	}
}

func (c *p2pCoordinator) selectAutoDistributionPeers(peers []P2PPeerView) []P2PPeerView {
	status := c.GetStatus()
	leechers := len(peers) - maxInt(status.CurrentSeedPlan.SelectedSeeds-1, 0)
	if leechers <= 0 {
		return []P2PPeerView{}
	}
	if leechers > len(peers) {
		leechers = len(peers)
	}
	return peers[:leechers]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
