package app

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"discovery/app/core/memlimit"
	"discovery/app/core/processutil"
)

// idleReleaseDelay é o tempo CONTÍNUO de ociosidade (nenhuma operação ativa)
// antes de liberar caches e devolver memória ao SO. Curto demais causaria
// liberação/reconstrução a cada pausa entre comandos; longo demais não ajudaria
// a máquina em repouso — que é justamente o caso da maioria dos agentes.
const idleReleaseDelay = 5 * time.Minute

// deferredRestartState tracks pending deferred restart state.
type deferredRestartState struct {
	mu           sync.Mutex
	deferCount   int
	maxDefers    int
	deferMinutes int
	message      string
	timer        *time.Timer
}

// cancelDeferredRestart cancela qualquer restart adiado pendente.
func (a *App) cancelDeferredRestart() {
	if a.deferredRestart == nil {
		return
	}
	a.deferredRestart.mu.Lock()
	defer a.deferredRestart.mu.Unlock()
	if a.deferredRestart.timer != nil {
		a.deferredRestart.timer.Stop()
		a.deferredRestart.timer = nil
	}
	a.deferredRestart.deferCount = 0
}

// scheduleDeferredRestart agenda a re-exibição do prompt de restart após
// deferMinutes. Por padrão NAO força o restart (maxDefers=0 = sem limite): a
// acao so e forçada se o servidor enviar maxDefers > 0 explicitamente.
func (a *App) scheduleDeferredRestart(action string, pp powerCommandPayload) {
	if a.deferredRestart == nil {
		a.deferredRestart = &deferredRestartState{
			maxDefers:    0,
			deferMinutes: 60,
			message:      pp.Message,
		}
	}
	ds := a.deferredRestart
	ds.mu.Lock()

	if pp.MaxDefers > 0 {
		ds.maxDefers = pp.MaxDefers
	}
	if pp.DeferMinutes > 0 {
		ds.deferMinutes = pp.DeferMinutes
	}
	if pp.Message != "" {
		ds.message = pp.Message
	}

	ds.deferCount++

	if ds.maxDefers > 0 && ds.deferCount >= ds.maxDefers {
		ds.mu.Unlock()
		a.Logs.Append(fmt.Sprintf("[agent] %s-defer [FORCE] maxDefers=%d atingido — restart forçado", action, ds.maxDefers))
		go func() {
			a.executeSystemPowerAction(context.Background(), action, 0, true, ds.message)
		}()
		return
	}

	delaySeconds := pp.DelaySeconds
	if delaySeconds <= 0 {
		delaySeconds = 300
	}
	deferMinutes := ds.deferMinutes
	msg := ds.message
	// M3: o callback do time.AfterFunc roda em goroutine própria — captura os
	// contadores AQUI sob lock; ler ds.deferCount/ds.maxDefers dentro do
	// callback (fora do lock) era data race com scheduleDeferredRestart/cancel.
	deferCount := ds.deferCount
	maxDefers := ds.maxDefers

	// Cancela timer anterior se existir
	if ds.timer != nil {
		ds.timer.Stop()
	}

	ds.timer = time.AfterFunc(time.Duration(deferMinutes)*time.Minute, func() {
		a.Logs.Append(fmt.Sprintf("[agent] %s-defer [RETRY] defer=%d/%d — re-exibindo prompt", action, deferCount, maxDefers))
		if pp.NotifyUser {
			// Reexibe o aviso nativo com contador; "defer" reagenda, o resto
			// (procedido/força) executa direto.
			switch a.showNativePowerCountdown(action, delaySeconds, msg, pp.Force) {
			case "defer":
				a.scheduleDeferredRestart(action, pp)
				return
			default:
				a.executeSystemPowerAction(context.Background(), action, 0, pp.Force, msg)
				return
			}
		}
		// Sem aviso configurado: timer interno, sem diálogo nativo do Windows.
		if delaySeconds > 0 {
			silent := pp
			silent.DelaySeconds = delaySeconds
			silent.Message = msg
			a.scheduleSilentPowerAction(action, silent)
			return
		}
		a.executeSystemPowerAction(context.Background(), action, 0, pp.Force, "")
	})

	ds.mu.Unlock()

	a.Logs.Append(fmt.Sprintf("[agent] %s-defer [SCHED] adiado para daqui %dmin (defer=%d/%d)", action, deferMinutes, deferCount, maxDefers))
}

func (a *App) beginActivity(activity string) func() {
	a.ActivityMu.Lock()
	a.ActiveOps++
	shouldLeaveIdle := a.ActiveOps == 1
	a.ActivityMu.Unlock()

	if shouldLeaveIdle {
		supported := a.applyIdleMode(false)
		if supported {
			a.Logs.Append("[efficiency] modo eficiencia desativado: " + activity)
		}
	}

	return func() {
		a.ActivityMu.Lock()
		if a.ActiveOps > 0 {
			a.ActiveOps--
		}
		shouldEnterIdle := a.ActiveOps == 0
		a.ActivityMu.Unlock()

		if shouldEnterIdle {
			supported := a.applyIdleMode(true)
			if supported {
				a.Logs.Append("[efficiency] modo eficiencia ativado (aguardo)")
			}
		}
	}
}

func (a *App) applyIdleMode(idle bool) bool {
	if !efficiencyModeEnabled {
		a.ActivityMu.Lock()
		a.IdleKnown = true
		a.IdleCapable = false
		a.LastIdle = false
		a.ActivityMu.Unlock()
		a.updateTrayIdleState(false, false)
		return false
	}

	a.ActivityMu.Lock()
	sameState := a.LastIdle == idle && a.IdleKnown
	if sameState {
		supported := a.IdleCapable
		a.ActivityMu.Unlock()
		return supported
	}
	a.LastIdle = idle
	a.ActivityMu.Unlock()

	supported, err := processutil.SetEfficiencyMode(idle)
	a.ActivityMu.Lock()
	a.IdleKnown = true
	a.IdleCapable = supported
	a.ActivityMu.Unlock()

	if err != nil {
		a.Logs.Append("[efficiency] erro ao alterar modo: " + err.Error())
	}

	if idle {
		// Reduz o working set imediatamente (barato) e agenda a liberação
		// profunda (caches + devolução de páginas ao SO) para depois de um
		// período contínuo de ociosidade.
		if trimErr := processutil.TrimCurrentProcessWorkingSet(); trimErr != nil {
			a.Logs.Append("[efficiency] erro ao reduzir memoria: " + trimErr.Error())
		}
		a.scheduleIdleRelease()
	} else {
		a.cancelIdleRelease()
	}

	a.updateTrayIdleState(idle, supported)
	return supported
}

// scheduleIdleRelease agenda a liberação profunda de memória. Idempotente:
// agendar duas vezes não cria dois timers.
func (a *App) scheduleIdleRelease() {
	a.idleReleaseMu.Lock()
	defer a.idleReleaseMu.Unlock()
	if a.idleReleaseTimer != nil {
		return
	}
	a.idleReleaseTimer = time.AfterFunc(idleReleaseDelay, func() {
		a.idleReleaseMu.Lock()
		a.idleReleaseTimer = nil
		a.idleReleaseMu.Unlock()

		// Confirma que continua ocioso (pode ter começado um comando no meio).
		a.ActivityMu.Lock()
		stillIdle := a.ActiveOps == 0 && a.LastIdle
		a.ActivityMu.Unlock()
		if !stillIdle {
			return
		}
		a.releaseIdleMemory()
	})
}

// cancelIdleRelease cancela a liberação agendada (voltou a haver atividade).
func (a *App) cancelIdleRelease() {
	a.idleReleaseMu.Lock()
	if a.idleReleaseTimer != nil {
		a.idleReleaseTimer.Stop()
		a.idleReleaseTimer = nil
	}
	a.idleReleaseMu.Unlock()
}

// releaseIdleMemory solta o que é caro manter em repouso: caches em memória
// (inclui a política efetiva da app-store, que chega a dezenas de MB com 27k
// itens), o page cache do SQLite e as páginas livres do heap de volta ao SO.
//
// NÃO usa clearMemoryCaches() de propósito: aquela versão (usada no
// close-to-tray da UI) também zera o InvCache, o que no serviço poderia forçar
// uma nova coleta de inventário (PowerShell/WMI) logo após uma janela de
// ociosidade. O snapshot de inventário é pequeno (~840 KB) e não compensa.
func (a *App) releaseIdleMemory() {
	a.AgentInfo.Invalidate()
	a.AppStorePolicy.Invalidate()
	if a.CoreAgent.DB != nil {
		if err := a.CoreAgent.DB.ShrinkMemory(); err != nil {
			log.Printf("[efficiency] shrink_memory na ociosidade: %v", err)
		}
	}
	// Rate-limited (no máximo 1x/10min) — nunca chamado em rajada.
	memlimit.Relax()
	log.Println("[efficiency] caches liberados e memória devolvida ao SO (ociosidade)")
}
