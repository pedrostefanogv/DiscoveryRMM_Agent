package app

import (
	"context"
	"fmt"
	"strings"

	"discovery/internal/database"
)

// HeadlessP2PService reuses the application P2P stack without Wails startup.
// It is intended for the Windows Service runtime.
type HeadlessP2PService struct {
	app        *App
	unregister func()
	started    bool
}

func NewHeadlessP2PService(logf func(string)) *HeadlessP2PService {
	a := NewApp(AppStartupOptions{DebugMode: true})
	svc := &HeadlessP2PService{
		app:        a,
		unregister: func() {},
	}
	if logf != nil {
		svc.unregister = a.logs.subscribe(func(line string) {
			if strings.TrimSpace(line) == "" {
				return
			}
			logf(line)
		})
	}
	return svc
}

func (s *HeadlessP2PService) SetDB(db *database.DB) {
	if s == nil || s.app == nil {
		return
	}
	s.app.db = db
	if db == nil {
		s.app.consolEngine = nil
		return
	}
	agentID := strings.TrimSpace(s.app.GetDebugConfig().AgentID)
	s.app.consolEngine = newConsolidationEngine(db, agentID)
}

func (s *HeadlessP2PService) Run(ctx context.Context) error {
	if s == nil || s.app == nil {
		return fmt.Errorf("servico P2P headless indisponivel")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.app.SetContext(ctx)
	if s.app.debugSvc != nil {
		// Recarrega a configuracao a cada execucao do worker e resolve credenciais
		// de bootstrap caso o agente ainda esteja em estado generico.
		s.app.debugSvc.LoadConnectionConfigFromProduction()
		s.app.debugSvc.BootstrapAgentCredentialsFromInstallerConfig(ctx)
	}
	if s.app.db != nil {
		s.app.consolEngine = newConsolidationEngine(s.app.db, strings.TrimSpace(s.app.GetDebugConfig().AgentID))
	}

	if !s.started {
		s.started = true
		go s.app.StartP2PTelemetryLoop(ctx)
		if !isAgentConfigured() && s.localAutoProvisioningAllowed() {
			go s.app.RunOnboardingLoop(ctx)
		}
	}

	s.app.p2pCoord.Run(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("runtime P2P finalizou sem cancelamento de contexto")
}

func (s *HeadlessP2PService) Close() {
	if s == nil {
		return
	}
	if s.unregister != nil {
		s.unregister()
		s.unregister = nil
	}
}

// localAutoProvisioningAllowed verifica o flag autoProvisioning persistido em
// config.json. Quando ausente, o padrão é permitir (true) — assim a decisão
// final fica com a feature flag do servidor (AgentConfiguration.DiscoveryEnabled).
// Quando explicitamente false, o agente não inicia o loop de onboarding local
// mesmo estando não configurado, atuando como kill-switch local de zero-touch.
func (s *HeadlessP2PService) localAutoProvisioningAllowed() bool {
	if s == nil || s.app == nil {
		return true
	}
	cfg, _, err := loadInstallerConfig()
	if err != nil {
		return true
	}
	if cfg.AutoProvisioning == nil {
		return true
	}
	allowed := *cfg.AutoProvisioning
	if !allowed {
		s.app.logs.append("[onboarding] autoProvisioning=false em config.json: loop de zero-touch desabilitado localmente")
	}
	return allowed
}
