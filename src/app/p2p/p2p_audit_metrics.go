package p2p

import (
	"fmt"
	"strings"
	"time"
)

// ── Métricas de bytes e auditoria ───────────────────────────────────────────
//
// Parte viva do antigo p2p_replication.go. A maquinaria de replicação PUSH
// (fila, worker, auto-distribuição e o protocolo /artifact/replicate) foi
// removida por ser código morto: o modo é pull-only e todos os pontos de
// entrada retornavam "push desabilitado". Este arquivo mantém apenas o que
// outros caminhos usam: contadores de bytes e o log/auditoria do P2P.

func (c *Coordinator) recordBytesServed(size int64) {
	if size <= 0 {
		return
	}
	c.mu.Lock()
	c.metrics.BytesServed += size
	c.mu.Unlock()
}

func (c *Coordinator) recordBytesDownloaded(size int64) {
	if size <= 0 {
		return
	}
	c.mu.Lock()
	c.metrics.BytesDownloaded += size
	c.mu.Unlock()
}

// recordStaleManifest incrementa o contador de manifests stale detectados.
// Usado para monitorar a frequência do problema em produção e avaliar a
// eficácia das correções de validação de cache.
func (c *Coordinator) recordStaleManifest() {
	c.mu.Lock()
	c.metrics.StaleManifestDetected++
	c.mu.Unlock()
}

func (c *Coordinator) appendAudit(action, artifactName, peerAgentID, source string, success bool, message string) {
	// Artifacts do selfupdate (selfupdate-<sha256>.exe) são rotulados com
	// source "selfupdate" para distinguir no audit/telemetria das transferências
	// de automação de apps de terceiros — sem propagar o source por todas as
	// assinaturas de download.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(artifactName)), "selfupdate-") {
		source = "selfupdate"
	}
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

	if c.deps != nil {
		c.deps.Log(formatP2PAuditLogLine(event))
	}
}

func formatP2PAuditLogLine(event P2PAuditEvent) string {
	status := "ok"
	if !event.Success {
		status = "erro"
	}
	action := strings.TrimSpace(event.Action)
	if action == "" {
		action = "unknown"
	}
	artifact := strings.TrimSpace(event.ArtifactName)
	if artifact == "" {
		artifact = "-"
	}
	peer := strings.TrimSpace(event.PeerAgentID)
	if peer == "" {
		peer = "-"
	}
	source := strings.TrimSpace(event.Source)
	if source == "" {
		source = "-"
	}
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = "-"
	}
	return fmt.Sprintf("[p2p][audit] status=%s action=%s artifact=%s peer=%s source=%s msg=%s",
		status, action, artifact, peer, source, message)
}
