package app

import (
	"context"
	"fmt"
	"time"
)

const (
	// apiHealthProbeEvery é a cadência do health-check da API HTTP.
	apiHealthProbeEvery = 20 * time.Second
	// apiHealthProbeInitial atrasa a primeira checagem para não competir com o
	// bootstrap (config/credenciais) nem contar falha espúria no boot.
	apiHealthProbeInitial = 15 * time.Second
)

// runAPIHealthProbe monitora a API HTTP periodicamente. Requisito de
// conectividade: o agent fica ONLINE somente quando o transporte NATS (nativo
// ou wss) E a API HTTP respondem; qualquer um dos dois fora ⇒ offline.
func (a *App) runAPIHealthProbe(ctx context.Context) {
	if a == nil || a.ApiClientSvc == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(apiHealthProbeInitial):
	}

	ticker := time.NewTicker(apiHealthProbeEvery)
	defer ticker.Stop()
	for {
		a.probeAPIOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// probeAPIOnce roda um probe e, quando o estado da API MUDA, propaga o estado
// efetivo (NATS ∧ API) para o suporte, a UI e o tray.
func (a *App) probeAPIOnce(ctx context.Context) {
	if a == nil || a.ApiClientSvc == nil {
		return
	}
	was := a.ApiClientSvc.APIReachable()
	err := a.ApiClientSvc.ProbeAPI(ctx)
	now := a.ApiClientSvc.APIReachable()
	if err != nil {
		_, _, failStreak := a.ApiClientSvc.APIHealth()
		// Diagnóstico sem spam: registra a 1ª falha da sequência e depois a cada
		// ~5 min (15 probes de 20s) enquanto a API continuar inacessível. Sem
		// isso o motivo real (timeout x HTTP 5xx) ficava invisível no log.
		if failStreak == 1 || failStreak%15 == 0 {
			a.Logs.Append(fmt.Sprintf("[connectivity] health-check da API falhou (falhas consecutivas=%d): %v", failStreak, err))
		}
	}
	if was == now {
		return
	}
	if now {
		a.emitEffectiveConnectivity("API respondendo novamente — conectividade reavaliada")
		return
	}
	a.emitEffectiveConnectivity("API inacessivel — agent marcado offline")
}

// effectiveConnectivity combina os dois canais: transporte NATS (nativo ou
// wss) e API HTTP. O agent é considerado conectado apenas quando ambos estão
// de pé.
func (a *App) effectiveConnectivity() (connected bool, transport string, apiReachable bool) {
	transportConnected := false
	if a.AgentConn != nil {
		st := a.AgentConn.GetStatus()
		transportConnected = st.Connected
		transport = st.Transport
	}
	apiReachable = true
	if a.ApiClientSvc != nil {
		apiReachable = a.ApiClientSvc.APIReachable()
	}
	return transportConnected && apiReachable, transport, apiReachable
}

// emitEffectiveConnectivity recalcula o estado combinado, avisa o suporte
// (somente-consulta) e emite agent:connectivity + atualiza tray.
func (a *App) emitEffectiveConnectivity(logMsg string) {
	if a == nil {
		return
	}
	connected, transport, apiReachable := a.effectiveConnectivity()
	if logMsg != "" {
		a.Logs.Append("[connectivity] " + logMsg)
	}
	if a.SupportSvc != nil {
		a.SupportSvc.NotifyConnectivity(connected)
	}
	a.EmitEvent("agent:connectivity", map[string]any{
		"connected":    connected,
		"transport":    transport,
		"apiReachable": apiReachable,
		"reason":       logMsg,
	})
	a.syncTrayVisualState()
	a.updateTrayTooltip()
	a.updateTrayMenu()
}

// applyAPIHealth força o status para offline quando a API HTTP está
// inacessível, mesmo com o NATS de pé.
//
// Só se aplica ao processo que RODA o probe (o serviço, via runAPIHealthProbe):
// na UI companion o ApiClientSvc local nunca é sondado e, se algum caminho
// futuro marcasse o health como indisponível ali, o tray/status da UI passaria a
// forçar offline com o serviço online — exatamente o falso negativo corrigido
// nesta revisão.
func (a *App) applyAPIHealth(st AgentStatus) AgentStatus {
	if a == nil || a.ApiClientSvc == nil || !a.RuntimeFlags.ServiceMode {
		return st
	}
	if a.ApiClientSvc.APIReachable() {
		return st
	}
	st.Connected = false
	st.OnlineReason = "API inacessivel"
	return st
}
