//go:build windows

package remotesession

import (
	"log"
	"sync"
	"time"

	"discovery/app/core/screen"
)

// ── Bloqueio de entrada do host remoto (KVM input lock) ──
//
// O viewer manda o comando pelo canal .control (inputLock) e o agent aplica o
// bloqueio no desktop remoto por hooks de baixo nível, engolindo apenas eventos
// FÍSICOS: o SendInput do operador continua passando (ver
// screen/input_lock_windows.go).
//
// FAIL-SAFE (requisito central): a máquina nunca pode ficar sem teclado/mouse.
//  1. liberação explícita em todos os teardowns (SessionScreen.Stop,
//     InputController, stop via stdin, liveness, shutdown);
//  2. LEASE renovado por qualquer frame do viewer (ping/pong do .control):
//     sem renovação por N segundos, o watchdog libera sozinho — cobre o caso
//     "worker vivo porém travado", que nenhum defer alcança;
//  3. o Windows libera o bloqueio quando o processo/thread que o aplicou morre
//     (o engine roda numa thread pinada — ver screen.BlockInputSystem).
const (
	inputLockDefaultLeaseSeconds = 60
	inputLockMinLeaseSeconds     = 15
	inputLockMaxLeaseSeconds     = 600
	inputLockMaxHardCapSeconds   = 3600
)

// Pontos de injeção/efeito (vars para stub nos testes).
var (
	blockSystemInput   = screen.BlockInputSystem
	unblockSystemInput = screen.UnblockInputSystem
	newLockIndicator   = screen.NewTrayIndicator

	inputLockWatchdogTick = time.Second
)

// InputLockState é o estado publicado ao viewer (inputLockChanged).
type InputLockState struct {
	Locked                bool
	Method                string
	LeaseRemainingSeconds int
	Reason                string
}

// Payload converte o estado para o payload do envelope de controle.
func (s InputLockState) Payload() map[string]any {
	p := map[string]any{"locked": s.Locked}
	if s.Method != "" {
		p["method"] = s.Method
	}
	if s.LeaseRemainingSeconds > 0 {
		p["leaseRemainingSeconds"] = s.LeaseRemainingSeconds
	}
	if s.Reason != "" {
		p["reason"] = truncateLockReason(s.Reason)
	}
	return p
}

// truncateLockReason limita o motivo ao orçamento do envelope de controle
// (sessioncontrol.MaxEnvelopeBytes = 512). Sem isto um erro longo do Windows
// faria Encode falhar e o viewer nunca receberia o estado — ficaria com o
// cadeado "travado" sem qualquer feedback.
func truncateLockReason(reason string) string {
	const maxRunes = 200
	runes := []rune(reason)
	if len(runes) <= maxRunes {
		return reason
	}
	return string(runes[:maxRunes]) + "..."
}

// lockIndicator é o indicador na máquina acessada (bandeja + balloon).
type lockIndicator interface {
	Show(message string) error
	Hide() error
	Close()
}

// InputLockManager mantém o estado do bloqueio de UMA sessão de tela.
type InputLockManager struct {
	sessionID string
	publish   func(InputLockState)

	// Injetáveis nos testes.
	now     func() time.Time
	block   func() (string, error)
	unblock func() error
	newInd  func() (lockIndicator, error)
	tick    time.Duration

	mu         sync.Mutex
	locked     bool
	method     string
	leaseSecs  int
	leaseUntil time.Time
	hardUntil  time.Time
	indicator  lockIndicator
	// closed fecha o manager: impede que um applyLock EM VOO (block() já
	// executado) marque a entrada como bloqueada DEPOIS do Close — sem isso a
	// máquina remota ficaria sem teclado/mouse, pois o watchdog já parou e
	// nenhum teardown voltaria a liberar.
	closed bool

	stopCh    chan struct{}
	doneCh    chan struct{}
	closeOnce sync.Once
}

// inputLockOptions injeta relógio/efeitos ANTES de o watchdog iniciar.
// Campos mutados depois do start seriam data race REAL (o watchdog lê now/
// unblock continuamente) — por isso a injeção é feita na construção.
type inputLockOptions struct {
	now     func() time.Time
	block   func() (string, error)
	unblock func() error
	newInd  func() (lockIndicator, error)
	tick    time.Duration
}

// NewInputLockManager cria o gerenciador e inicia o watchdog do lease.
func NewInputLockManager(sessionID string, publish func(InputLockState)) *InputLockManager {
	return newInputLockManager(sessionID, publish, inputLockOptions{})
}

// newInputLockManager é o construtor com pontos de injeção (testes).
func newInputLockManager(sessionID string, publish func(InputLockState), opts inputLockOptions) *InputLockManager {
	m := &InputLockManager{
		sessionID: sessionID,
		publish:   publish,
		now:       time.Now,
		block:     blockSystemInput,
		unblock:   unblockSystemInput,
		newInd: func() (lockIndicator, error) {
			return newLockIndicator()
		},
		tick:   inputLockWatchdogTick,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	if opts.now != nil {
		m.now = opts.now
	}
	if opts.block != nil {
		m.block = opts.block
	}
	if opts.unblock != nil {
		m.unblock = opts.unblock
	}
	if opts.newInd != nil {
		m.newInd = opts.newInd
	}
	if opts.tick > 0 {
		m.tick = opts.tick
	}
	go m.watchdogLoop()
	return m
}

// HandleCommand processa o payload de inputLock vindo do viewer.
func (m *InputLockManager) HandleCommand(payload map[string]any) {
	if payload == nil {
		return
	}
	if query, _ := payload["query"].(bool); query {
		m.PublishCurrent("query")
		return
	}
	locked, ok := payload["locked"].(bool)
	if !ok {
		m.publishState(InputLockState{Locked: false, Reason: "invalid_command"})
		return
	}
	if !locked {
		m.Release("viewer_request")
		return
	}

	lease := inputLockDefaultLeaseSeconds
	if v := toInt(payload["leaseSeconds"], 0); v > 0 {
		lease = clampLockSeconds(v, inputLockMinLeaseSeconds, inputLockMaxLeaseSeconds)
	}
	hard := 0
	if v := toInt(payload["maxSeconds"], 0); v > 0 {
		hard = clampLockSeconds(v, inputLockMinLeaseSeconds, inputLockMaxHardCapSeconds)
	}
	m.applyLock(lease, hard)
}

// applyLock bloqueia a entrada (idempotente: já bloqueado apenas renova o lease).
func (m *InputLockManager) applyLock(leaseSeconds, hardCapSeconds int) {
	now := m.now()

	m.mu.Lock()
	if m.locked {
		m.leaseSecs = leaseSeconds
		m.leaseUntil = now.Add(time.Duration(leaseSeconds) * time.Second)
		if hardCapSeconds > 0 {
			m.hardUntil = now.Add(time.Duration(hardCapSeconds) * time.Second)
		}
		m.mu.Unlock()
		m.publishState(m.snapshot("renewed"))
		return
	}
	m.mu.Unlock()

	// Efeito fora do lock: instalar os hooks é rápido, mas não bloquear o estado.
	method, err := m.block()
	if err != nil {
		log.Printf("[remote-session][input-lock] falha ao bloquear entrada (sessao=%s): %v", m.sessionID, err)
		m.publishState(InputLockState{Locked: false, Reason: "block_failed: " + err.Error()})
		return
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		// Sessão encerrada enquanto o block() rodava: desfaz AGORA. Ninguém
		// mais vai liberar (watchdog parado, Stop já passou).
		log.Printf("[remote-session][input-lock] sessao fechou durante o bloqueio — desfazendo (sessao=%s)", m.sessionID)
		if uerr := m.unblock(); uerr != nil {
			log.Printf("[remote-session][input-lock] unblock de desfazimento falhou (sessao=%s): %v", m.sessionID, uerr)
		}
		return
	}
	m.locked = true
	m.method = method
	m.leaseSecs = leaseSeconds
	m.leaseUntil = now.Add(time.Duration(leaseSeconds) * time.Second)
	if hardCapSeconds > 0 {
		m.hardUntil = now.Add(time.Duration(hardCapSeconds) * time.Second)
	} else {
		m.hardUntil = time.Time{}
	}
	m.mu.Unlock()

	// Indicador na máquina acessada é BEST-EFFORT: se falhar, o bloqueio vale.
	if ind := m.ensureIndicator(); ind != nil {
		if err := ind.Show("Teclado e mouse bloqueados pelo suporte remoto"); err != nil {
			log.Printf("[remote-session][input-lock] indicador na maquina remota indisponivel: %v", err)
		}
	}

	log.Printf("[remote-session][input-lock] entrada BLOQUEADA (metodo=%s lease=%ds) sessao=%s", method, leaseSeconds, m.sessionID)
	m.publishState(m.snapshot("locked"))
}

// Renew estende o lease — chamado a cada frame do viewer (ping/pong).
func (m *InputLockManager) Renew() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.locked {
		return
	}
	lease := m.leaseSecs
	if lease <= 0 {
		lease = inputLockDefaultLeaseSeconds
	}
	m.leaseUntil = m.now().Add(time.Duration(lease) * time.Second)
}

// Release libera a entrada. Idempotente e seguro em qualquer caminho de
// encerramento (Stop, stdin "stop", liveness, shutdown, lease expirado).
func (m *InputLockManager) Release(reason string) {
	m.mu.Lock()
	if !m.locked {
		m.mu.Unlock()
		return
	}
	ind := m.indicator
	m.mu.Unlock()

	if err := m.unblock(); err != nil {
		// A máquina PODE continuar bloqueada. Manter o estado "travado" é a
		// verdade: o operador vê o cadeado vermelho, pode tentar de novo e o
		// lease/watchdog continuam valendo. Se o processo morrer, o Windows
		// libera de qualquer forma.
		log.Printf("[remote-session][input-lock] falha ao liberar entrada (sessao=%s): %v", m.sessionID, err)
		if ind != nil {
			if serr := ind.Show("Falha ao liberar a entrada — a máquina pode continuar bloqueada"); serr != nil {
				log.Printf("[remote-session][input-lock] falha ao atualizar indicador: %v", serr)
			}
		}
		m.publishState(InputLockState{
			Locked: true,
			Method: m.methodName(),
			Reason: "unblock_failed: " + err.Error(),
		})
		return
	}

	m.mu.Lock()
	m.locked = false
	m.method = ""
	m.leaseUntil = time.Time{}
	m.hardUntil = time.Time{}
	m.mu.Unlock()

	if ind != nil {
		if err := ind.Hide(); err != nil {
			log.Printf("[remote-session][input-lock] falha ao remover indicador: %v", err)
		}
	}
	log.Printf("[remote-session][input-lock] entrada LIBERADA (%s) sessao=%s", reason, m.sessionID)
	m.publishState(InputLockState{Locked: false, Reason: reason})
}

// methodName lê o método de bloqueio atual (thread-safe).
func (m *InputLockManager) methodName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.method
}

// PublishCurrent publica o estado atual (usado no início da sessão e no query).
func (m *InputLockManager) PublishCurrent(reason string) {
	m.publishState(m.snapshot(reason))
}

// Close encerra o watchdog, libera a entrada e fecha o indicador. Idempotente.
func (m *InputLockManager) Close() {
	m.closeOnce.Do(func() {
		// MARCA closed ANTES de parar o watchdog: um applyLock em voo precisa
		// ver a flag ao voltar do block() e desfazer o bloqueio.
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		close(m.stopCh)
		<-m.doneCh
		m.Release("manager_closed")

		m.mu.Lock()
		ind := m.indicator
		m.indicator = nil
		m.mu.Unlock()
		if ind != nil {
			ind.Close()
		}
	})
}

// ensureIndicator cria o indicador sob demanda (só quando algo é bloqueado).
func (m *InputLockManager) ensureIndicator() lockIndicator {
	m.mu.Lock()
	if m.indicator != nil {
		ind := m.indicator
		m.mu.Unlock()
		return ind
	}
	if m.closed {
		// Fast-path: nem cria a janela/thread do indicador.
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	ind, err := m.newInd()
	if err != nil {
		log.Printf("[remote-session][input-lock] indicador local indisponivel: %v", err)
		return nil
	}

	m.mu.Lock()
	switch {
	case m.closed:
		// Manager fechado durante a criação: não deixa janela/thread órfã.
		m.mu.Unlock()
		ind.Close()
		return nil
	case m.indicator == nil:
		m.indicator = ind
		m.mu.Unlock()
		return ind
	default:
		// Outra goroutine criou primeiro: descarta o duplicado.
		existing := m.indicator
		m.mu.Unlock()
		ind.Close()
		return existing
	}
}

// snapshot devolve o estado atual com o lease restante.
func (m *InputLockManager) snapshot(reason string) InputLockState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.locked {
		return InputLockState{Locked: false, Reason: reason}
	}
	st := InputLockState{Locked: true, Method: m.method, Reason: reason}
	if !m.leaseUntil.IsZero() {
		remaining := int(m.leaseUntil.Sub(m.now()).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		st.LeaseRemainingSeconds = remaining
	}
	return st
}

func (m *InputLockManager) publishState(st InputLockState) {
	if m.publish == nil {
		return
	}
	m.publish(st)
}

// watchdogLoop libera a entrada quando o lease (ou o teto) expira sem renovação.
func (m *InputLockManager) watchdogLoop() {
	defer close(m.doneCh)
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			now := m.now()
			m.mu.Lock()
			locked := m.locked
			leaseExpired := locked && !m.leaseUntil.IsZero() && !now.Before(m.leaseUntil)
			hardExpired := locked && !m.hardUntil.IsZero() && !now.Before(m.hardUntil)
			m.mu.Unlock()
			if !locked {
				continue
			}
			switch {
			case hardExpired:
				log.Printf("[remote-session][input-lock] teto de bloqueio atingido (sessao=%s) — liberando por seguranca", m.sessionID)
				m.Release("max_duration")
			case leaseExpired:
				m.Release("lease_expired")
			}
		}
	}
}

// clampLockSeconds limita um valor em segundos à faixa aceita.
func clampLockSeconds(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
