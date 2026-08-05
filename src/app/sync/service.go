package sync

import (
	"context"
	"time"

	"discovery/app/core/agentconn"
	debug "discovery/app/debug"
	"discovery/app/p2pmeta"
)

// Service agrega todos os componentes do domínio Sync/offline em uma única
// unidade: Coordinator (sync-manifest), Rollout (modos offline), Backoff
// (tráfego não essencial + conectividade), CommandOutbox e P2PTelemetryOutbox
// (persistência offline). O *App mantém uma única referência a este agregador,
// e os bridges na raiz delegam para ele.
//
// Futuramente este tipo pode ser registrado como um Service Wails3
// (ServiceStartup/ServiceShutdown) para ciclo de vida próprio.
type Service struct {
	deps SyncDeps

	Coordinator        *Coordinator
	Rollout            *Rollout
	Backoff            *Backoff
	CommandOutbox      *CommandOutbox
	P2PTelemetryOutbox *P2PTelemetryOutbox
}

// NewService cria o agregador do domínio Sync/offline com as dependências
// injetadas.
func NewService(deps SyncDeps) *Service {
	rollout := NewRollout(deps)
	return &Service{
		deps:               deps,
		Coordinator:        New(deps),
		Rollout:            rollout,
		Backoff:            NewBackoff(deps),
		CommandOutbox:      NewCommandOutbox(deps, rollout),
		P2PTelemetryOutbox: NewP2PTelemetryOutbox(deps, rollout),
	}
}

// ── Delegados do Coordinator ──

// Run inicia o loop de sincronização até o contexto ser cancelado.
func (s *Service) Run(ctx context.Context) {
	if s == nil || s.Coordinator == nil {
		return
	}
	s.Coordinator.Run(ctx)
}

// HandlePing processa um ping de invalidação de sync.
func (s *Service) HandlePing(ping agentconn.SyncPing) {
	if s == nil || s.Coordinator == nil {
		return
	}
	s.Coordinator.HandlePing(ping)
}

// ReconcileFromManifest busca o sync-manifest e enfileira recursos alterados.
func (s *Service) ReconcileFromManifest(ctx context.Context, source string) error {
	if s == nil || s.Coordinator == nil {
		return nil
	}
	return s.Coordinator.ReconcileFromManifest(ctx, source)
}

// SetPollEvery ajusta o intervalo de polling.
func (s *Service) SetPollEvery(value time.Duration) {
	if s == nil || s.Coordinator == nil {
		return
	}
	s.Coordinator.SetPollEvery(value)
}

// ── Delegados do Backoff ──

// HandleGlobalPong processa um tenant.global.pong.
func (s *Service) HandleGlobalPong(pong agentconn.GlobalPongMessage) {
	if s == nil || s.Backoff == nil {
		return
	}
	s.Backoff.HandleGlobalPong(pong)
}

// NonCriticalBackoffWindow retorna o tempo restante de adiamento, se houver.
func (s *Service) NonCriticalBackoffWindow() (time.Duration, bool, string) {
	if s == nil || s.Backoff == nil {
		return 0, false, ""
	}
	return s.Backoff.NonCriticalBackoffWindow()
}

// NonCriticalBackoffStatus retorna o estado bruto do backoff.
func (s *Service) NonCriticalBackoffStatus() (time.Time, bool, string) {
	if s == nil || s.Backoff == nil {
		return time.Time{}, false, ""
	}
	return s.Backoff.NonCriticalBackoffStatus()
}

// ResolveAgentConnectivity enriquece o status do agent com o sinal online.
func (s *Service) ResolveAgentConnectivity(status debug.AgentStatus) debug.AgentStatus {
	if s == nil || s.Backoff == nil {
		return status
	}
	return s.Backoff.ResolveAgentConnectivity(status)
}

// ── Delegados do CommandOutbox ──

// EnqueueCommandResultOutbox persiste um resultado de comando no outbox.
func (s *Service) EnqueueCommandResultOutbox(transport, dispatchID, commandID string, exitCode int, output, errText, sendError string) error {
	if s == nil || s.CommandOutbox == nil {
		return nil
	}
	return s.CommandOutbox.Enqueue(transport, dispatchID, commandID, exitCode, output, errText, sendError)
}

// ListDueCommandResultOutbox retorna os itens do outbox prontos para envio.
func (s *Service) ListDueCommandResultOutbox(transport string, now time.Time, limit int) ([]agentconn.CommandResultOutboxItem, error) {
	if s == nil || s.CommandOutbox == nil {
		return nil, nil
	}
	return s.CommandOutbox.ListDue(transport, now, limit)
}

// MarkSentCommandResultOutbox marca um item do outbox como enviado.
func (s *Service) MarkSentCommandResultOutbox(id int64) error {
	if s == nil || s.CommandOutbox == nil {
		return nil
	}
	return s.CommandOutbox.MarkSent(id)
}

// RescheduleCommandResultOutbox reagenda um item do outbox.
func (s *Service) RescheduleCommandResultOutbox(id int64, attempts int, nextAttemptAt time.Time, lastError string) error {
	if s == nil || s.CommandOutbox == nil {
		return nil
	}
	return s.CommandOutbox.Reschedule(id, attempts, nextAttemptAt, lastError)
}

// ── Delegados do P2PTelemetryOutbox ──

// EnqueueP2PTelemetryOutbox persiste um payload de telemetria no outbox.
func (s *Service) EnqueueP2PTelemetryOutbox(payload p2pmeta.TelemetryPayload, sendErr error) error {
	if s == nil || s.P2PTelemetryOutbox == nil {
		return nil
	}
	return s.P2PTelemetryOutbox.Enqueue(payload, sendErr)
}

// DrainP2PTelemetryOutbox envia os itens do outbox prontos para envio.
func (s *Service) DrainP2PTelemetryOutbox(ctx context.Context, limit int) error {
	if s == nil || s.P2PTelemetryOutbox == nil {
		return nil
	}
	return s.P2PTelemetryOutbox.Drain(ctx, limit)
}
