package app

import (
	"context"

	"github.com/wailsapp/wails/v3/pkg/application"

	"discovery/app/core/remotedebug"
	"discovery/app/core/remotesession"
)

// ── Adapters thin para Services Wails v3 ────────────────────────────────
//
// Os domínios remotedebug e remotesession são packages puros, desacoplados do
// Wails. Para registrá-los como Services Wails v3 separados (dividindo a carga
// de trabalho do App), estes adapters thin implementam a interface exata do
// Wails (ServiceStartup/ServiceShutdown/ServiceName) e delegam o ciclo de vida
// para os managers. Assim os domínios continuam sem dependência do package
// `application`.

// remoteDebugService registra o remotedebug.Manager como Service Wails v3.
type remoteDebugService struct {
	mgr *remotedebug.Manager
}

// ServiceName retorna o nome do service para logging.
func (s *remoteDebugService) ServiceName() string {
	if s.mgr == nil {
		return "remotedebug.Manager"
	}
	return s.mgr.ServiceName()
}

// ServiceStartup delega para o ciclo de vida do remotedebug.Manager.
func (s *remoteDebugService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	if s.mgr == nil {
		return nil
	}
	return s.mgr.Startup(ctx)
}

// ServiceShutdown delega para o ciclo de vida do remotedebug.Manager.
func (s *remoteDebugService) ServiceShutdown() error {
	if s.mgr == nil {
		return nil
	}
	return s.mgr.Shutdown()
}

// remoteSessionService registra o remotesession.Manager como Service Wails v3.
type remoteSessionService struct {
	mgr *remotesession.Manager
}

// ServiceName retorna o nome do service para logging.
func (s *remoteSessionService) ServiceName() string {
	if s.mgr == nil {
		return "remotesession.Manager"
	}
	return s.mgr.ServiceName()
}

// ServiceStartup delega para o ciclo de vida do remotesession.Manager.
func (s *remoteSessionService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	if s.mgr == nil {
		return nil
	}
	return s.mgr.Startup(ctx)
}

// ServiceShutdown delega para o ciclo de vida do remotesession.Manager.
func (s *remoteSessionService) ServiceShutdown() error {
	if s.mgr == nil {
		return nil
	}
	return s.mgr.Shutdown()
}

// RemoteDebugService retorna o adapter Wails v3 para o domínio remote debug.
//
//wails:ignore
func (a *App) RemoteDebugService() *remoteDebugService {
	return &remoteDebugService{mgr: a.remoteDebug}
}

// RemoteSessionService retorna o adapter Wails v3 para o domínio de acesso remoto.
//
//wails:ignore
func (a *App) RemoteSessionService() *remoteSessionService {
	return &remoteSessionService{mgr: a.remoteSessionMgr}
}
