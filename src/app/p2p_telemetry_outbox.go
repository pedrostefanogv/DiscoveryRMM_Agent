package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"discovery/app/p2p"
)

const (
	p2pTelemetryDrainLimit = p2p.TelemetryDrainLimit
)

// buildP2PTelemetryPayload monta o payload de telemetria P2P a partir do
// estado do coordinator. Permanece no *App porque depende de p2pCoord.
func (a *App) buildP2PTelemetryPayload() (P2PTelemetryPayload, error) {
	if a.p2pCoord == nil {
		return P2PTelemetryPayload{}, fmt.Errorf("coordinator P2P indisponível")
	}
	status := a.GetP2PDebugStatus()
	payload := P2PTelemetryPayload{
		AgentID:         strings.TrimSpace(a.GetDebugConfig().AgentID),
		CollectedAtUTC:  time.Now().UTC().Format(time.RFC3339),
		Metrics:         status.Metrics,
		CurrentSeedPlan: status.CurrentSeedPlan,
		KnownPeers:      a.p2pCoord.CountKnownPeers(),
		ConnectedPeers:  a.p2pCoord.CountConnectedLibp2pPeers(),
	}
	if info, err := a.GetAgentInfo(); err == nil {
		payload.SiteID = strings.TrimSpace(info.SiteID)
	}

	// Artifacts locais (truncado a 500 itens)
	artifacts, _ := a.p2pCoord.ListArtifacts()
	if len(artifacts) > 500 {
		artifacts = artifacts[:500]
	}
	for _, art := range artifacts {
		payload.Artifacts = append(payload.Artifacts, P2PArtifactPresenceItem{
			ArtifactID:   art.ArtifactID,
			ArtifactName: art.ArtifactName,
			Sha256:       art.ChecksumSHA256,
			SizeBytes:    art.SizeBytes,
			CachedAtUtc:  art.ModifiedAtUTC,
		})
	}

	// Carga do host: reusa coleta de métricas existente do heartbeat
	cfg := a.GetP2PConfig()
	if cfg.Enabled {
		load := a.p2pCoord.CollectHostLoad()
		payload.HostLoad = &load
	}

	return payload, nil
}

// enqueueP2PTelemetryOutbox delega para o outbox do pacote sync.
func (a *App) enqueueP2PTelemetryOutbox(payload P2PTelemetryPayload, sendErr error) error {
	if a.syncSvc == nil {
		return nil
	}
	return a.syncSvc.EnqueueP2PTelemetryOutbox(payload, sendErr)
}

// drainP2PTelemetryOutbox delega para o outbox do pacote sync.
func (a *App) drainP2PTelemetryOutbox(ctx context.Context, limit int) error {
	if a.syncSvc == nil {
		return nil
	}
	return a.syncSvc.DrainP2PTelemetryOutbox(ctx, limit)
}

// marshalP2PTelemetryPayload delega a serialização (com limite de tamanho)
// para o pacote p2p.
func marshalP2PTelemetryPayload(payload P2PTelemetryPayload) ([]byte, error) {
	return p2p.MarshalTelemetryPayload(payload)
}
