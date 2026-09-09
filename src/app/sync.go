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
	if a.AgentConfigSvc == nil {
		a.AgentConfigSvc = agentconfig.New(agentconfig.FetchDeps{
			GetDebugConfig: a.GetDebugConfig,
		})
	}
	result, err := a.AgentConfigSvc.Fetch(ctx)
	if err != nil {
		// fallback to cached config when request fails
		_ = a.loadCachedAgentConfiguration()
		return err
	}

	if result.HasZeroTouchPendingFlag && result.ZeroTouchPending {
		if a.setZeroTouchApprovalPending(true) {
			a.Logs.Append("[sync] dispositivo provisionado e aguardando aprovacao da equipe de TI para integracao com o servidor")
		}
		return nil
	}

	if a.setZeroTouchApprovalPending(false) {
		a.Logs.Append("[sync] aprovacao recebida; integracao com o servidor liberada")
	}

	if a.CoreAgent.DB != nil {
		_ = a.CoreAgent.DB.CacheSet("agent_configuration_raw", result.RawBody, 30*24*time.Hour)
	}

	a.setAgentConfiguration(result.Config)
	a.applyStartupThrottleConfig()
	if a.DebugSvc != nil {
		changed, applyErr := a.DebugSvc.ApplyRemoteConnectionSecurity(
			result.Config.NatsServerHost,
			result.Config.NatsServerHostInternal,
			result.Config.NatsUseWssExternal,
			result.Config.EnforceTlsHashValidation,
			result.Config.HandshakeEnabled,
			result.Config.ApiTlsCertHash,
			result.Config.NatsTlsCertHash,
		)
		if applyErr != nil {
			a.Logs.Append("[sync] falha ao aplicar seguranca remota de transporte: " + applyErr.Error())
		} else if changed {
			a.Logs.Append("[sync] segurança de transporte aplicada e reconexão solicitada")
		}
	}
	a.Logs.Append("[sync] configuração do agent atualizada")
	return nil
}
