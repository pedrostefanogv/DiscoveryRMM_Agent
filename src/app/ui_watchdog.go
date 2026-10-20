package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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
	// uiWatchdogSpawnGrace é o período após um relançamento em que o watchdog
	// NÃO conta verificações de "processo vivo sem IPC": a UI pode levar ~1 min
	// para nascer e conectar (autostart no tray com WebView2 adiado — nos logs
	// de 2026-10-20 a UI nasceu em ~54s). Sem essa tolerância o watchdog matava
	// a UI no parto e entrava no ciclo matar/relançar (18:21:44 spawn →
	// 18:22:35+ "processos vivos sem IPC" → kill → relançar → repetir).
	uiWatchdogSpawnGrace = 90 * time.Second
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
	// MarkUISpawn (opcional) registra PROGRESSO de UI para a tolerância do
	// convite de chat sem interface (ver supportChatUISpawnInProgress).
	MarkUISpawn func(reason string)
	// KillZombieUI encerra as UIs sem IPC (esperando elas saírem) e devolve
	// quantas foram encerradas.
	KillZombieUI func() int
	// Log registra no logstore do agente.
	Log func(message string)
	// Now é o relógio (injetável nos testes).
	Now func() time.Time

	// ── Multiusuário (opcional) ──────────────────────────────────────────
	//
	// Quando ActiveSessions e SpawnUISession são fornecidos, o watchdog passa
	// a raciocinar POR SESSÃO: enumera as sessões interativas ativas (WTS) e
	// relança a UI em cada uma que ainda não tem processo, sem duplicar. Sem
	// eles (nil), mantém-se o comportamento single-session anterior — é o que
	// os testes genéricos e o fallback de console usam.
	//
	// ActiveSessions devolve os IDs das sessões ativas (nunca inclui a sessão
	// 0 de serviços). UISessionCounts mapeia sessionID → quantos processos
	// discovery-agent.exe existem nela. SpawnUISession relança a UI na sessão
	// informada (WTSQueryUserToken + CreateProcessAsUser via exec).
	ActiveSessions  func() []uint32
	UISessionCounts func() map[uint32]int
	SpawnUISession  func(sessionID uint32, reason string) error
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

	// ── Estado por sessão (modo multiusuário) ──
	// sessionMisses conta verificações seguidas SEM UI em cada sessão;
	// sessionLastSpawn aplica cooldown individual (um usuário novo não deve
	// ser barrado pelo relançamento feito na sessão de outro).
	sessionMisses    map[uint32]int
	sessionLastSpawn map[uint32]time.Time
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

	// Toda saída deste método registra o DESFECHO.
	//
	// BUG RELATADO (08:20, convite com o agent fechado no tray): o watchdog não
	// logou absolutamente nada por 70 s — nem pedido, nem relançada, nem erro.
	// Sem o desfecho, um relançamento barrado (sem sessão, cooldown) era
	// indistinguível de um watchdog morto.
	if w.deps.HasUI() {
		w.misses = 0
		w.zombieChecks = 0
		w.logOnceLocked("pedido-com-ui", "pedido de relançamento da UI ("+reason+") ignorado: interface JA conectada no IPC")
		return
	}
	// Log único por estado: um convite sem UI é evento raro, mas repetir a mesma
	// linha a cada tentativa bloqueada pelo cooldown seria ruído no logstore.
	w.logOnceLocked("pedido-sem-ui", "pedido de relançamento da UI ("+reason+") sem UI conectada")
	if w.sessionMode() {
		sessions := w.activeSessionsLocked()
		if len(sessions) == 0 {
			w.logOnceLocked("pedido-sem-sessao", "pedido de relançamento da UI ("+reason+
				") nao relancou: sem sessao de usuario ativa")
			return
		}
		// Um pedido sem UI vale para TODAS as sessões sem interface: o convite
		// pode ser para o usuário de qualquer uma delas.
		w.spawnSessionsLocked(sessions, "pedido:"+reason, uiWatchdogRequestCooldown, true)
		return
	}
	w.spawnLocked("pedido:"+reason, uiWatchdogRequestCooldown)
}

// checkLocked é a verificação periódica (chamada com w.mu preso).
func (w *uiWatchdog) checkLocked() {
	if w.sessionMode() {
		w.checkSessionsLocked()
		return
	}
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
		// travou. O limiar separa os dois casos — e um relançamento recente nem
		// começa a contar (a UI ainda pode estar nascendo).
		if w.spawnRecentlyLocked() {
			w.misses = 0
			w.logOnceLocked("spawn-recente", "UI relançada recentemente; aguardando a primeira conexão IPC antes de tratar como travada")
			return
		}
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

// sessionMode informa se o watchdog tem as dependências para raciocinar por
// sessão (multiusuário). Sem elas, vale o comportamento legado.
func (w *uiWatchdog) sessionMode() bool {
	return w.deps.ActiveSessions != nil && w.deps.SpawnUISession != nil
}

// spawnRecentlyLocked informa se algum relançamento (global ou por sessão)
// aconteceu há menos de uiWatchdogSpawnGrace. Chamado com w.mu preso.
func (w *uiWatchdog) spawnRecentlyLocked() bool {
	now := w.deps.Now()
	if !w.lastSpawn.IsZero() && now.Sub(w.lastSpawn) < uiWatchdogSpawnGrace {
		return true
	}
	for _, last := range w.sessionLastSpawn {
		if !last.IsZero() && now.Sub(last) < uiWatchdogSpawnGrace {
			return true
		}
	}
	return false
}

// activeSessionsLocked devolve as sessões interativas ativas, já normalizadas
// (sem duplicatas, sem sessão 0, ordenadas) e poda o estado de sessões que
// deixaram de existir (logoff/encerramento).
func (w *uiWatchdog) activeSessionsLocked() []uint32 {
	sessions := normalizedUISessions(w.deps.ActiveSessions())
	keep := make(map[uint32]bool, len(sessions))
	for _, sid := range sessions {
		keep[sid] = true
	}
	for sid := range w.sessionMisses {
		if !keep[sid] {
			delete(w.sessionMisses, sid)
		}
	}
	for sid := range w.sessionLastSpawn {
		if !keep[sid] {
			delete(w.sessionLastSpawn, sid)
		}
	}
	return sessions
}

// checkSessionsLocked é a verificação periódica no modo multiusuário.
//
// Regras:
//   - sem sessão ativa: nada a relançar (a próxima sessão logada será vista no
//     próximo tick) e nenhum cooldown é consumido;
//   - UI conectada no IPC: as sessões que JÁ têm processo são ignoradas, mas
//     sessões ativas sem UI ainda são atendidas (é o caso de dois usuários:
//     um com a UI aberta e outro sem);
//   - processo vivo sem IPC: UI travada → encerra tudo e relança em cada
//     sessão ativa (o lock de instância é POR SESSÃO, então uma UI nova por
//     sessão é o resultado correto);
//   - sem processo: relança em cada sessão ativa depois do limiar.
func (w *uiWatchdog) checkSessionsLocked() {
	if w.sessionMisses == nil {
		w.sessionMisses = make(map[uint32]int)
	}
	if w.sessionLastSpawn == nil {
		w.sessionLastSpawn = make(map[uint32]time.Time)
	}

	sessions := w.activeSessionsLocked()
	if len(sessions) == 0 {
		w.misses = 0
		w.zombieChecks = 0
		w.logOnceLocked("sem-sessao", "sem sessão de usuário ativa — UI será relançada quando alguém fizer logon")
		return
	}

	counts := map[uint32]int{}
	if w.deps.UISessionCounts != nil {
		counts = w.deps.UISessionCounts()
	}

	if w.deps.HasUI() {
		// Existe pelo menos uma UI conectada: não é caso de zumbi. Ainda assim,
		// sessões sem processo precisam da sua própria UI.
		w.misses = 0
		w.zombieChecks = 0
		w.zombieStreak = 0
		missing := sessionsMissingUI(sessions, counts)
		if len(missing) == 0 {
			w.lastNoticeKey = ""
			return
		}
		w.spawnSessionsLocked(missing, "sessao-sem-ui", uiWatchdogRequestCooldown, false)
		return
	}

	if uiProcessTotal(counts) > 0 {
		// Processo vivo sem NENHUMA UI conectada: ou está subindo, ou travou.
		// Relançamento recente nem começa a contar (a UI ainda pode estar
		// nascendo — ver uiWatchdogSpawnGrace).
		if w.spawnRecentlyLocked() {
			w.misses = 0
			w.logOnceLocked("spawn-recente", "UI relançada recentemente; aguardando a primeira conexão IPC antes de tratar como travada")
			return
		}
		w.misses = 0
		w.zombieChecks++
		if w.zombieChecks < uiWatchdogZombieThreshold {
			return
		}
		w.zombieChecks = 0
		w.deps.Log(fmt.Sprintf(
			"UI sem IPC por %d verificações com %d processo(s) vivo(s) — tratando como travada e relançando no tray em %d sessão(ões)",
			uiWatchdogZombieThreshold, uiProcessTotal(counts), len(sessions)))
		if killed := w.deps.KillZombieUI(); killed > 0 {
			w.deps.Log(fmt.Sprintf("UI travada encerrada (%d processo(s))", killed))
		}
		// Depois do kill não existe UI em sessão nenhuma: relança em todas.
		cooldown := uiWatchdogRequestCooldown
		if w.zombieStreak > 0 {
			cooldown = uiWatchdogSpawnCooldown
		}
		w.zombieStreak++
		w.spawnSessionsLocked(sessions, "zumbi", cooldown, true)
		return
	}

	w.zombieChecks = 0
	w.spawnSessionsLocked(sessionsMissingUI(sessions, counts), "ausente", uiWatchdogSpawnCooldown, false)
}

// spawnSessionsLocked relança a UI nas sessões informadas, com limiar e
// cooldown POR SESSÃO (force=true pula o limiar de verificações — usado depois
// de matar a UI travada e em pedidos imediatos).
func (w *uiWatchdog) spawnSessionsLocked(sessions []uint32, reason string, cooldown time.Duration, force bool) {
	if len(sessions) == 0 {
		// Pedido imediato (force) sem sessão nenhuma: o silêncio aqui era o
		// "watchdog não logou nada".
		if force {
			w.logOnceLocked("sessoes-vazio:"+reason, "relancamento da UI nao executado ("+reason+"): nenhuma sessao para atender")
		}
		return
	}
	if w.sessionMisses == nil {
		w.sessionMisses = make(map[uint32]int)
	}
	if w.sessionLastSpawn == nil {
		w.sessionLastSpawn = make(map[uint32]time.Time)
	}
	now := w.deps.Now()
	for _, sid := range sessions {
		if !force {
			w.sessionMisses[sid]++
			if w.sessionMisses[sid] < uiWatchdogMissThreshold {
				continue
			}
		}
		if last, ok := w.sessionLastSpawn[sid]; ok && now.Sub(last) < cooldown {
			// Cooldown ativo: registra com a espera restante quando o pedido é
			// IMEDIATO (convite) — no caminho periódico seria ruído a cada tick.
			if force {
				remaining := cooldown - now.Sub(last)
				w.logOnceLocked("cooldown:"+reason+":"+strconv.FormatUint(uint64(sid), 10),
					fmt.Sprintf("relancamento da UI nao executado (%s, sessão %d): cooldown ativo (restam %s)",
						reason, sid, remaining.Round(time.Second)))
			}
			continue
		}
		// Marca ANTES de tentar: um spawn que falha não pode virar retry apertado.
		w.sessionLastSpawn[sid] = now
		if err := w.deps.SpawnUISession(sid, reason); err != nil {
			w.deps.Log(fmt.Sprintf("falha ao relançar a UI (sessão %d, %s): %v", sid, reason, err))
			continue
		}
		w.sessionMisses[sid] = 0
		w.lastNoticeKey = ""
		w.deps.Log(fmt.Sprintf("UI relançada minimizada no tray (sessão %d, %s)", sid, reason))
	}
}

// normalizedUISessions limpa a lista de sessões: remove duplicatas, a sessão 0
// (serviços — não tem desktop/usuário) e 0xFFFFFFFF (constante de "sem
// sessão"), e ordena por ID para o log/plano ficarem determinísticos.
func normalizedUISessions(raw []uint32) []uint32 {
	seen := make(map[uint32]bool, len(raw))
	out := make([]uint32, 0, len(raw))
	for _, sid := range raw {
		if sid == 0 || sid == 0xFFFFFFFF || seen[sid] {
			continue
		}
		seen[sid] = true
		out = append(out, sid)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// sessionsMissingUI devolve as sessões ativas que ainda NÃO têm processo da UI.
func sessionsMissingUI(sessions []uint32, counts map[uint32]int) []uint32 {
	out := make([]uint32, 0, len(sessions))
	for _, sid := range sessions {
		if counts[sid] <= 0 {
			out = append(out, sid)
		}
	}
	return out
}

// uiProcessTotal soma os processos de UI em todas as sessões.
func uiProcessTotal(counts map[uint32]int) int {
	total := 0
	for _, n := range counts {
		if n > 0 {
			total += n
		}
	}
	return total
}

// spawnLocked relança a UI respeitando o cooldown (chamado com w.mu preso).
func (w *uiWatchdog) spawnLocked(reason string, cooldown time.Duration) {
	now := w.deps.Now()
	if !w.lastSpawn.IsZero() && now.Sub(w.lastSpawn) < cooldown {
		// Já houve tentativa recente. O tick é de 60s e o silêncio ali é de
		// propósito, mas um pedido IMEDIATO (convite) barrado pelo cooldown não
		// pode sumir do log — era um dos buracos do "watchdog sem nenhuma
		// mensagem por 70 s".
		if strings.HasPrefix(reason, "pedido:") {
			w.logOnceLocked("cooldown:"+reason,
				"relancamento da UI nao executado ("+reason+"): cooldown ativo (restam "+
					(cooldown-now.Sub(w.lastSpawn)).Round(time.Second).String()+")")
		}
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
	// Progresso de UI para a tolerância do convite de chat sem interface: com
	// a UI nascendo, o prazo de 25 s é prorrogado em vez de recusar o convite
	// (ver supportChatUISpawnInProgress).
	if w.deps.MarkUISpawn != nil {
		w.deps.MarkUISpawn(reason)
	}
	// Um relançamento bem-sucedido invalida os avisos de estado anteriores
	// (cooldown/sem-sessão) — senão um pedido barrado uma vez nunca mais seria
	// reportado.
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
