// Package sessionlive provides a transport-agnostic liveness contract for
// interactive sessions between two peers (e.g. remote debug browser <-> agent).
//
// O pacote é deliberadamente cego ao transporte: quem o usa injeta a função de
// envio do sinal (NATS, WebSocket, HTTP...) e liga a chegada dos sinais do
// peer em NotePeerSignal. Assim o mesmo núcleo serve ao debug remoto, à sessão
// de tela e a qualquer outro recurso interativo futuro.
//
// Contrato:
//   - cada peer envia um sinal (ping) a cada Interval e registra em
//     NotePeerSignal os sinais recebidos do outro lado (pong/ping);
//   - se o peer nunca sinalizou, vale a janela de partida (InitialGrace) —
//     não é compatibilidade com versões antigas: é a corrida real entre o
//     comando de start e o primeiro sinal do viewer (launcher -> popup ->
//     credenciais -> connect -> primeiro ping);
//   - após o primeiro sinal, a tolerância é MissesAllowed*Interval;
//   - a sessão pode ser estendida indefinidamente (sliding deadline) enquanto
//     o peer estiver vivo, com teto absoluto em MaxDeadline.
package sessionlive

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Config parametriza o contrato de liveness de um peer.
type Config struct {
	// Interval é a cadência do tick (ping) e também a base de medida de
	// "sinais perdidos".
	Interval time.Duration
	// MissesAllowed é quantos intervalos sem sinal do peer encerram a sessão.
	MissesAllowed int
	// InitialGrace é a janela para o PRIMEIRO sinal do peer.
	InitialGrace time.Duration
	// MaxDeadline é o teto absoluto da sessão (startedAt + duração máxima
	// configurada na instalação). Zero = sem teto.
	MaxDeadline time.Time
	// CloseWithoutPeerSignal: quando true, encerra a sessão após InitialGrace se
	// o peer NUNCA sinalizou (comportamento do remote debug). Quando false
	// (acesso remoto), nunca fecha por ausência antes do primeiro sinal — o
	// chamador mantém o prazo original do start, o que preserva viewers antigos
	// que ainda não enviam ping. Depois do primeiro sinal, os dois modos fecham
	// após MissesAllowed*Interval.
	CloseWithoutPeerSignal bool
}

// Peer rastreia a presença do outro lado e calcula o deadline deslizante.
// Seguro para uso concorrente.
type Peer struct {
	mu           sync.Mutex
	cfg          Config
	startedAt    time.Time
	lastSignalAt time.Time
	peerSeen     bool
}

// NewPeer cria um Peer com a janela de partida ancorada em startedAt.
func NewPeer(cfg Config, startedAt time.Time) *Peer {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return &Peer{cfg: normalizeConfig(cfg), startedAt: startedAt.UTC()}
}

func normalizeConfig(cfg Config) Config {
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.MissesAllowed <= 0 {
		cfg.MissesAllowed = 3
	}
	if cfg.InitialGrace <= 0 {
		cfg.InitialGrace = 60 * time.Second
	}
	return cfg
}

// NotePeerSignal registra um sinal recebido do peer.
func (p *Peer) NotePeerSignal(now time.Time) {
	if p == nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSignalAt = now.UTC()
	p.peerSeen = true
}

// PeerSeen reporta se o peer já sinalizou pelo menos uma vez.
func (p *Peer) PeerSeen() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peerSeen
}

// ShouldClose avalia se a sessão deve ser encerrada por ausência do peer.
// Retorna (true, motivo) quando sim.
//
// Estados:
//   - peer nunca sinalizou → fecha apenas após InitialGrace (partida);
//   - peer sinalizou       → fecha após MissesAllowed*Interval sem sinal.
func (p *Peer) ShouldClose(now time.Time) (bool, string) {
	if p == nil {
		return false, ""
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.peerSeen {
		if p.lastSignalAt.IsZero() {
			// Não deve acontecer com peerSeen=true; não fecha por erro de estado.
			return false, ""
		}
		if now.Sub(p.lastSignalAt) > time.Duration(p.cfg.MissesAllowed)*p.cfg.Interval {
			return true, "peer-timeout"
		}
		return false, ""
	}

	if !p.cfg.CloseWithoutPeerSignal {
		// Acesso remoto: sem primeiro sinal do viewer, o encerramento é do
		// prazo original do start (o runner também avalia esse fallback).
		return false, ""
	}

	if now.Sub(p.startedAt) > p.cfg.InitialGrace {
		return true, "peer-timeout"
	}
	return false, ""
}

// Alive reporta se o peer está saudável (sinalizou e não passou da janela).
func (p *Peer) Alive(now time.Time) bool {
	if closed, _ := p.ShouldClose(now); closed {
		return false
	}
	return p.PeerSeen()
}

// Deadline calcula o deadline deslizante (agora + ttl, clamped no teto
// absoluto). Quando o peer nunca sinalizou, retorna zero — o consumidor deve
// manter o deadline original (não estender), porque a sessão encerra pelo
// prazo original ao fim de InitialGrace.
func (p *Peer) Deadline(now time.Time, ttl time.Duration) time.Time {
	if p == nil {
		return time.Time{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.peerSeen {
		return time.Time{}
	}
	deadline := now.Add(ttl)
	if !p.cfg.MaxDeadline.IsZero() && deadline.After(p.cfg.MaxDeadline) {
		deadline = p.cfg.MaxDeadline
	}
	return deadline
}

// MaxDeadline retorna o teto absoluto (zero = sem teto).
func (p *Peer) MaxDeadline() time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.MaxDeadline
}

// ExceededMaxDeadline informa se agora já passou do teto absoluto.
func (p *Peer) ExceededMaxDeadline(now time.Time) bool {
	if p == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cfg.MaxDeadline.IsZero() {
		return false
	}
	return now.UTC().After(p.cfg.MaxDeadline)
}

// Acessores unexported para o Runner (evitam espelhar o Config em RunnerOptions).
func (p *Peer) interval() time.Duration { p.mu.Lock(); defer p.mu.Unlock(); return p.cfg.Interval }
func (p *Peer) missesAllowed() int      { p.mu.Lock(); defer p.mu.Unlock(); return p.cfg.MissesAllowed }
func (p *Peer) initialGrace() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.InitialGrace
}

// RunnerOptions define os callbacks do Runner.
type RunnerOptions struct {
	// OnTick é chamado a cada Interval (o peer envia seu sinal aqui).
	OnTick func(ctx context.Context)
	// OnPeerLost é chamado quando o peer parou de responder; após retornar o
	// Runner encerra e não chama OnTick de novo.
	OnPeerLost func(reason string)
	// Logf recebe linhas de diagnóstico (opcional).
	Logf func(string)
}

// Runner roda o loop de liveness de um peer.
type Runner struct {
	peer    *Peer
	opts    RunnerOptions
	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewRunner cria um Runner para o peer informado.
func NewRunner(peer *Peer, opts RunnerOptions) *Runner {
	// done nasce já fechado: Wait() retorna imediatamente quando Run() ainda
	// não rodou (ex.: peer nil) em vez de bloquear para sempre.
	done := make(chan struct{})
	close(done)
	return &Runner{peer: peer, opts: opts, done: done}
}

// Run executa o loop de liveness e bloqueia até o contexto ser cancelado, o
// peer parar de responder ou Stop ser chamado. Use em goroutine própria.
//
// Apenas um loop por Runner: chamadas concorrentes subsequentes retornam
// imediatamente sem criar um segundo ticker.
func (r *Runner) Run(parent context.Context) {
	if r == nil || r.peer == nil {
		return
	}

	ctx, cancel := context.WithCancel(parent)
	r.mu.Lock()
	if r.started {
		// Apenas um loop por Runner: chamadas subsequentes (concorrentes ou
		// depois de Stop) retornam imediatamente em vez de sobrepor o loop.
		r.mu.Unlock()
		cancel()
		return
	}
	r.started = true
	r.cancel = cancel
	done := make(chan struct{})
	r.done = done
	r.mu.Unlock()

	defer close(done)
	defer cancel()

	if r.opts.Logf != nil {
		r.opts.Logf("runner de liveness iniciado: interval=" + r.peer.interval().String() +
			" misses=" + strconv.Itoa(r.peer.missesAllowed()) +
			" grace=" + r.peer.initialGrace().String())
	}

	ticker := time.NewTicker(r.peer.interval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if closed, reason := r.peer.ShouldClose(now); closed {
				if r.opts.OnPeerLost != nil {
					r.opts.OnPeerLost(reason)
				}
				return
			}
			if r.opts.OnTick != nil {
				r.opts.OnTick(ctx)
			}
		}
	}
}

// Stop cancela o loop. Idempotente.
func (r *Runner) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Wait bloqueia até o loop encerrar (retorna imediatamente se nunca iniciou).
func (r *Runner) Wait() {
	if r == nil {
		return
	}
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return
	}
	<-done
}
