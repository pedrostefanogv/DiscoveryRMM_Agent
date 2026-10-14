package app

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ── Watchdog da UI companion ────────────────────────────────────────────────
//
// O core vive no serviço Windows (SYSTEM) e continua de pé mesmo sem interface.
// A UI (discovery-agent.exe) é um processo SEPARADO, na sessão do usuário, e
// hoje só é iniciada pela tarefa de logon do Task Scheduler — sem repetição e
// sem restart em falha. Se ela não sobe no logon, se o usuário a fecha (Sair no
// tray) ou se ela trava, NADA a traz de volta; o efeito visível é o suporte
// abrir um chat e o serviço responder "agent sem interface de usuario para
// confirmar o chat" (hasSupportChatUI), sem notificação nenhuma na máquina.
//
// Este watchdog cobre os três casos, sempre com a UI subindo MINIMIZADA no tray
// (--startup-minimized) e sem roubar foco:
//
//  1. processo ausente        → relança depois de N verificações seguidas;
//  2. processo vivo sem IPC   → UI travada (zumbi): encerra e relança;
//  3. convite de chat sem UI  → disparo imediato, sem esperar o próximo tick.
//
// Só roda no modo serviço: na UI standalone o próprio processo é a UI.
const (
	// uiWatchdogTickInterval é o intervalo da verificação periódica.
	uiWatchdogTickInterval = 60 * time.Second
	// uiWatchdogMissThreshold exige N verificações seguidas sem UI antes de
	// relançar o processo (evita brigar com a task de logon, que tem delay de
	// 30s, e com o boot da própria UI).
	uiWatchdogMissThreshold = 2
	// uiWatchdogZombieThreshold exige N verificações com processo vivo e SEM IPC
	// antes de considerar a UI travada.
	uiWatchdogZombieThreshold = 2
	// uiWatchdogSpawnCooldown é o intervalo mínimo entre relançamentos
	// automáticos (processo subindo e morrendo em sequência não vira tempestade).
	uiWatchdogSpawnCooldown = 5 * time.Minute
	// uiWatchdogRequestCooldown é o intervalo mínimo entre relançamentos
	// PEDIDOS (chegada de convite de chat sem UI).
	uiWatchdogRequestCooldown = 30 * time.Second
)

// StartupSourceWatchdog é a origem registrada na linha de comando da UI
// relançada pelo watchdog. É uma origem AUTOMÁTICA: main.go força o start
// minimizado e o single-instance não rouba foco (ver IsAutomaticStartupArgs).
const StartupSourceWatchdog = "watchdog"

// uiWatchdogDeps são as dependências do watchdog. São funções injetadas para
// que a lógica (limiares, cooldowns, decisão de matar/relançar) seja testável
// sem Windows, sem processos e sem Wails.
type uiWatchdogDeps struct {
	// HasUI informa se existe UI companion conectada no IPC.
	HasUI func() bool
	// ProcessCount informa quantos discovery-agent.exe existem agora.
	ProcessCount func() int
	// UserSessionAvailable informa se há sessão de console com usuário logado
	// (sem ela não há onde abrir a UI).
	UserSessionAvailable func() bool
	// SpawnUI relança a UI minimizada na sessão do usuário.
	SpawnUI func(reason string) error
	// KillZombieUI encerra as UIs sem IPC (esperando elas saírem) e devolve
	// quantas foram encerradas.
	KillZombieUI func() int
	// Log registra no logstore do agente.
	Log func(message string)
	// Now é o relógio (injetável nos testes).
	Now func() time.Time
}

// uiWatchdog relança a UI quando ela não está viva e conectada ao IPC.
type uiWatchdog struct {
	deps uiWatchdogDeps

	mu           sync.Mutex
	misses       int
	zombieChecks int
	// zombieStreak conta relançamentos consecutivos por UI travada SEM que a UI
	// volte a conectar — serve de backoff para não criar um ciclo patológico de
	// matar/relançar. Zera assim que uma UI conecta.
	zombieStreak int
	lastSpawn    time.Time
	// lastNoticeKey deduplica mensagens de estado (ver logOnceLocked).
	lastNoticeKey string
}

func newUIWatchdog(deps uiWatchdogDeps) *uiWatchdog {
	if deps.Log == nil {
		deps.Log = func(string) {}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.ProcessCount == nil {
		deps.ProcessCount = func() int { return 0 }
	}
	if deps.UserSessionAvailable == nil {
		deps.UserSessionAvailable = func() bool { return true }
	}
	if deps.HasUI == nil {
		deps.HasUI = func() bool { return false }
	}
	if deps.SpawnUI == nil {
		deps.SpawnUI = func(string) error { return nil }
	}
	if deps.KillZombieUI == nil {
		deps.KillZombieUI = func() int { return 0 }
	}
	return &uiWatchdog{deps: deps}
}

// Run roda a verificação periódica até o ctx ser cancelado.
func (w *uiWatchdog) Run(ctx context.Context) {
	if w == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(uiWatchdogTickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Tick()
		}
	}
}

// Tick faz uma verificação periódica (ver a lógica em checkLocked).
func (w *uiWatchdog) Tick() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.checkLocked()
}

// RequestSpawn pede um relançamento IMEDIATO (não espera o tick).
//
// Usado quando chega um convite de chat e o serviço sabe que não há UI para
// confirmá-lo: subir a UI agora é a chance de o PRÓXIMO convite já aparecer na
// máquina. Sem UI conectada e sem cooldown curto vencido, não faz nada.
func (w *uiWatchdog) RequestSpawn(reason string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.deps.HasUI() {
		w.misses = 0
		w.zombieChecks = 0
		return
	}
	// Log único por estado: um convite sem UI é evento raro, mas repetir a mesma
	// linha a cada tentativa bloqueada pelo cooldown seria ruído no logstore.
	w.logOnceLocked("pedido-sem-ui", "pedido de relançamento da UI ("+reason+") sem UI conectada")
	w.spawnLocked("pedido:"+reason, uiWatchdogRequestCooldown)
}

// checkLocked é a verificação periódica (chamada com w.mu preso).
func (w *uiWatchdog) checkLocked() {
	if w.deps.HasUI() {
		// Tudo em ordem: zera os contadores para que uma queda futura precise
		// novamente do limiar completo.
		w.misses = 0
		w.zombieChecks = 0
		w.zombieStreak = 0
		return
	}

	if alive := w.deps.ProcessCount(); alive > 0 {
		// Processo vivo, mas nenhuma UI conectada no IPC: ou está subindo, ou
		// travou. O limiar separa os dois casos.
		w.misses = 0
		w.zombieChecks++
		if w.zombieChecks < uiWatchdogZombieThreshold {
			return
		}
		w.zombieChecks = 0
		w.deps.Log(fmt.Sprintf(
			"UI sem IPC por %d verificações com %d processo(s) vivo(s) — tratando como travada e relançando no tray",
			uiWatchdogZombieThreshold, alive))
		if killed := w.deps.KillZombieUI(); killed > 0 {
			w.deps.Log(fmt.Sprintf("UI travada encerrada (%d processo(s))", killed))
		}
		// Primeira ocorrência: relança já (o processo travado acabou de morrer,
		// então não há risco de duplicar). REINCIDÊNCIA (a UI sobe e trava de
		// novo sem nunca conectar): usa o cooldown longo para não martelar a
		// máquina em ciclo matar/relançar.
		cooldown := uiWatchdogRequestCooldown
		if w.zombieStreak > 0 {
			cooldown = uiWatchdogSpawnCooldown
		}
		w.zombieStreak++
		w.spawnLocked("zumbi", cooldown)
		return
	}

	w.zombieChecks = 0
	w.misses++
	if w.misses < uiWatchdogMissThreshold {
		return
	}
	// Sem log aqui: o tick repete a cada 60s e, quando o cooldown ou a ausência
	// de usuário barram o relançamento, a mensagem "relançando" seria mentira.
	// Quem relata o desfecho é spawnLocked (sucesso, falha ou espera).
	w.spawnLocked("ausente", uiWatchdogSpawnCooldown)
}

// spawnLocked relança a UI respeitando o cooldown (chamado com w.mu preso).
func (w *uiWatchdog) spawnLocked(reason string, cooldown time.Duration) {
	now := w.deps.Now()
	if !w.lastSpawn.IsZero() && now.Sub(w.lastSpawn) < cooldown {
		// Já houve tentativa recente: silêncio de propósito (o tick é de 60s).
		return
	}
	if !w.deps.UserSessionAvailable() {
		// Sem usuário logado não existe sessão onde abrir a UI. Não consome o
		// cooldown: a próxima verificação tenta de novo assim que alguém logar.
		// logOnceLocked: numa máquina sem ninguém logado o tick roda por horas.
		w.logOnceLocked("sem-sessao", "sem sessão de usuário ativa — UI será relançada quando alguém fizer logon")
		return
	}

	// Marca ANTES de tentar: um spawn que falha não pode virar retry apertado.
	w.lastSpawn = now
	if err := w.deps.SpawnUI(reason); err != nil {
		w.deps.Log("falha ao relançar a UI (" + reason + "): " + err.Error())
		return
	}
	w.misses = 0
	w.lastNoticeKey = ""
	w.deps.Log("UI relançada minimizada no tray (" + reason + ")")
}

// logOnceLocked registra uma mensagem apenas quando ela muda de contexto: o tick
// de 60s repetiria a mesma linha indefinidamente (máquina sem usuário logado,
// cooldown ativo) e o logstore já é o canal de diagnóstico do agente.
func (w *uiWatchdog) logOnceLocked(key, message string) {
	if key == w.lastNoticeKey {
		return
	}
	w.lastNoticeKey = key
	w.deps.Log(message)
}

// requestUIWatchdogSpawn pede o relançamento imediato da UI, sem bloquear o
// chamador (o convite de chat é respondido na sequência).
func (a *App) requestUIWatchdogSpawn(reason string) {
	if a == nil {
		return
	}
	watchdog := a.uiWatchdog.Load()
	if watchdog == nil {
		return
	}
	go watchdog.RequestSpawn(reason)
}
