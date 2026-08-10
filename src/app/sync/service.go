package sync

import (
	"context"
	"log"
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
// Implementa ServiceStartup/ServiceShutdown (interfaces opcionais do Wails v3)
// para ciclo de vida próprio. Atualmente o *App chama estes métodos
// explicitamente em seu próprio startup/shutdown; quando as dependências
// forem totalmente desacopladas, o sync.Service poderá ser registrado como
// Service Wails3 separado em main.go.
type Service struct {
	deps SyncDeps

	Coordinator        *Coordinator
	Rollout            *Rollout
	Backoff            *Backoff
	CommandOutbox      *CommandOutbox
	P2PTelemetryOutbox *P2PTelemetryOutbox

	// ctx é o contexto de ciclo de vida, cancelado no shutdown.
	ctx    context.Context
	cancel context.CancelFunc
	// started indica se ServiceStartup foi chamado com sucesso.
	started bool
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

// ServiceName retorna o nome do service para logging.
func (s *Service) ServiceName() string {
	return "sync.Service"
}

// Startup prepara o contexto de ciclo de vida do domínio Sync.
// É chamado pelo *App em sua fase de startup (Phase 3, +10s).
//
// NOTA: Este método NÃO implementa a interface ServiceStartup do Wails v3
// (que requer application.ServiceOptions) para evitar dependência do pacote
// sync no Wails. O *App chama este método explicitamente. Quando o sync.Service
// for registrado como Service Wails3 separado, um adapter thin em app.go
// implementará a interface exata e delegará para este método.
func (s *Service) Startup(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.started {
		return nil
	}
	// Deriva um contexto cancelável do ctx recebido (lifetime da aplicação).
	ctx, cancel := context.WithCancel(ctx)
	s.ctx = ctx
	s.cancel = cancel
	s.started = true
	log.Println("[sync] Startup concluído")
	return nil
}

// Shutdown cancela o contexto de ciclo de vida do domínio Sync.
// É chamado pelo *App em seu shutdown.
func (s *Service) Shutdown() error {
	if s == nil || !s.started {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.started = false
	log.Println("[sync] Shutdown concluído")
	return nil
}

// Ctx retorna o contexto de ciclo de vida do service.
// Retorna context.Background() se Startup ainda não foi chamado.
func (s *Service) Ctx() context.Context {
	if s == nil || s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// ── Delegados do Coordinator ──

// Run inicia o loop de sincronização até o contexto ser cancelado.
// Usa o contexto de ciclo de vida (s.ctx) se Startup foi chamado;
// caso contrário, usa o ctx passado como argumento (compat).
func (s *Service) Run(ctx context.Context) {
	if s == nil || s.Coordinator == nil {
		return
	}
	runCtx := ctx
	if s.ctx != nil {
		runCtx = s.ctx
	}
	s.Coordinator.Run(runCtx)
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
