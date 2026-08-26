package app

import (
	"context"
	"time"

	"discovery/app/agentconfig"
)

// refreshAgentConfiguration busca a configuração do agent na API, aplica
// segurança de transporte remota e atualiza o runtime. Permanece no *App
// porque depende de muitos serviços concretos (agentConfigSvc, debugSvc,
// zero-touch, persistência). O coordinator (src/app/sync) delega para cá
// via sync.SyncDeps.RefreshAgentConfiguration.
func (a *App) refreshAgentConfiguration(ctx context.Context) error {
	if a.agentConfigSvc == nil {
		a.agentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
			GetDebugConfig: a.GetDebugConfig,
		})
	}
	result, err := a.agentConfigSvc.Fetch(ctx)
	if err != nil {
		// fallback to cached config when request fails
		_ = a.loadCachedAgentConfiguration()
		return err
	}

	if result.HasZeroTouchPendingFlag && result.ZeroTouchPending {
		if a.setZeroTouchApprovalPending(true) {
			a.logs.append("[sync] dispositivo provisionado e aguardando aprovacao da equipe de TI para integracao com o servidor")
		}
		return nil
	}

	if a.setZeroTouchApprovalPending(false) {
		a.logs.append("[sync] aprovacao recebida; integracao com o servidor liberada")
	}

	if a.db != nil {
		_ = a.db.CacheSet("agent_configuration_raw", result.RawBody, 30*24*time.Hour)
	}

	a.setAgentConfiguration(result.Config)
	a.applyStartupThrottleConfig()
	if a.debugSvc != nil {
		changed, applyErr := a.debugSvc.ApplyRemoteConnectionSecurity(
			result.Config.NatsServerHost,
			result.Config.NatsServerHostInternal,
			result.Config.NatsUseWssExternal,
			result.Config.EnforceTlsHashValidation,
			result.Config.HandshakeEnabled,
			result.Config.ApiTlsCertHash,
			result.Config.NatsTlsCertHash,
		)
		if applyErr != nil {
			a.logs.append("[sync] falha ao aplicar seguranca remota de transporte: " + applyErr.Error())
		} else if changed {
			a.logs.append("[sync] segurança de transporte aplicada e reconexão solicitada")
		}
	}
	a.logs.append("[sync] configuração do agent atualizada")
	return nil
}
