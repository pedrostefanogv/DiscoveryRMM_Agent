package sessionlive

import (
	"context"
	"testing"
	"time"
)

// TestPeer_InitialGraceClosesWhenPeerNeverSignals cobre a corrida de partida:
// enquanto o viewer nao conectou, o peer NAO encerra a sessao; apos a grace
// sem nenhum sinal, encerra com motivo peer-timeout.
func TestPeer_InitialGraceClosesWhenPeerNeverSignals(t *testing.T) {
	startedAt := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	cfg := Config{
		Interval:               5 * time.Second,
		MissesAllowed:          3,
		InitialGrace:           60 * time.Second,
		CloseWithoutPeerSignal: true,
	}
	p := NewPeer(cfg, startedAt)

	if p.PeerSeen() {
		t.Fatalf("peerSeen deveria comecar falso")
	}
	if closed, reason := p.ShouldClose(startedAt.Add(59 * time.Second)); closed {
		t.Fatalf("dentro da grace nao deveria fechar, got %q", reason)
	}
	if closed, _ := p.ShouldClose(startedAt.Add(60 * time.Second)); closed {
		t.Fatalf("no fim exato da grace ainda nao deveria fechar")
	}
	if closed, reason := p.ShouldClose(startedAt.Add(61 * time.Second)); !closed || reason != "peer-timeout" {
		t.Fatalf("apos a grace deveria fechar com peer-timeout, got closed=%t reason=%q", closed, reason)
	}
	if p.Alive(startedAt.Add(61 * time.Second)) {
		t.Fatalf("peer nao deveria estar vivo sem sinal")
	}
}

// TestPeer_NoCloseWithoutPeerSignal cobre o acesso remoto: sem o primeiro
// sinal do viewer, o peer NUNCA fecha por ausencia (viewers antigos nao
// enviam ping); o prazo original do start continua valendo no consumidor.
func TestPeer_NoCloseWithoutPeerSignal(t *testing.T) {
	startedAt := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	cfg := Config{
		Interval:      5 * time.Second,
		MissesAllowed: 3,
		InitialGrace:  60 * time.Second,
	}
	p := NewPeer(cfg, startedAt)

	for _, at := range []time.Duration{59 * time.Second, 61 * time.Second, 30 * time.Minute} {
		if closed, reason := p.ShouldClose(startedAt.Add(at)); closed {
			t.Fatalf("sem primeiro sinal nao deveria fechar (t=%s), got %q", at, reason)
		}
	}
	if p.Alive(startedAt.Add(time.Hour)) {
		t.Fatalf("Alive deveria ser false sem nenhum sinal do viewer")
	}

	// Depois do primeiro sinal, a janela normal volta a valer.
	p.NotePeerSignal(startedAt.Add(time.Minute))
	if closed, reason := p.ShouldClose(startedAt.Add(time.Minute + 16*time.Second)); !closed || reason != "peer-timeout" {
		t.Fatalf("apos o primeiro sinal deveria fechar, got closed=%t reason=%q", closed, reason)
	}
}

// TestPeer_MissesWindowClosesAfterLastSignal valida a politica de 3 sinais
// perdidos e o reset da janela a cada sinal.
func TestPeer_MissesWindowClosesAfterLastSignal(t *testing.T) {
	startedAt := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	cfg := Config{
		Interval:      5 * time.Second,
		MissesAllowed: 3,
		InitialGrace:  60 * time.Second,
	}
	p := NewPeer(cfg, startedAt)

	// Primeiro sinal dentro da grace.
	p.NotePeerSignal(startedAt.Add(2 * time.Second))
	if !p.PeerSeen() {
		t.Fatalf("peerSeen deveria ser true apos o primeiro sinal")
	}

	// Dentro de 15s ainda vivo.
	if closed, _ := p.ShouldClose(startedAt.Add(15 * time.Second)); closed {
		t.Fatalf("dentro de misses*interval nao deveria fechar")
	}
	// Depois de 15s sem sinal, fecha.
	if closed, reason := p.ShouldClose(startedAt.Add(18 * time.Second)); !closed || reason != "peer-timeout" {
		t.Fatalf("esperava peer-timeout, got closed=%t reason=%q", closed, reason)
	}

	// Um sinal novo reabre a janela.
	p.NotePeerSignal(startedAt.Add(20 * time.Second))
	if closed, _ := p.ShouldClose(startedAt.Add(32 * time.Second)); closed {
		t.Fatalf("sinal novo deveria ter reaberto a janela")
	}
}

// TestPeer_DeadlineSlidesUntilMaxDeadline valida o deadline deslizante e o
// clamp no teto absoluto.
func TestPeer_DeadlineSlidesUntilMaxDeadline(t *testing.T) {
	startedAt := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	cfg := Config{Interval: 5 * time.Second, MissesAllowed: 3, InitialGrace: 60 * time.Second}
	p := NewPeer(cfg, startedAt)

	// Sem sinal: nao estende (retorna zero) - consumidor mantem o prazo original.
	if got := p.Deadline(startedAt.Add(10*time.Second), 20*time.Minute); !got.IsZero() {
		t.Fatalf("sem sinal o deadline nao deveria ser estendido, got %s", got.Format(time.RFC3339))
	}

	p.NotePeerSignal(startedAt.Add(2 * time.Second))
	got := p.Deadline(startedAt.Add(10*time.Second), 20*time.Minute)
	want := startedAt.Add(10 * time.Second).Add(20 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("deadline = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	// Com teto: o clamp vence.
	pMax := NewPeer(Config{
		Interval:      5 * time.Second,
		MissesAllowed: 3,
		InitialGrace:  60 * time.Second,
		MaxDeadline:   startedAt.Add(time.Hour),
	}, startedAt)
	pMax.NotePeerSignal(startedAt.Add(time.Minute))
	gotMax := pMax.Deadline(startedAt.Add(50*time.Minute), 30*time.Minute)
	if !gotMax.Equal(startedAt.Add(time.Hour)) {
		t.Fatalf("deadline deveria clampar no teto, got %s", gotMax.Format(time.RFC3339))
	}

	// Depois do teto, ExceededMaxDeadline vira true.
	if !pMax.ExceededMaxDeadline(startedAt.Add(time.Hour + time.Second)) {
		t.Fatalf("esperava ExceededMaxDeadline=true apos o teto")
	}
	if pMax.ExceededMaxDeadline(startedAt.Add(30 * time.Minute)) {
		t.Fatalf("antes do teto ExceededMaxDeadline deveria ser false")
	}
}

func TestPeer_NilSafe(t *testing.T) {
	var p *Peer
	if p.PeerSeen() {
		t.Fatalf("nil Peer deveria ser seen=false")
	}
	if closed, _ := p.ShouldClose(time.Now()); closed {
		t.Fatalf("nil Peer nao deveria fechar")
	}
	if got := p.Deadline(time.Now(), time.Minute); !got.IsZero() {
		t.Fatalf("nil Peer nao deveria estender deadline")
	}
	p.NotePeerSignal(time.Now()) // no-op sem panic
	r := NewRunner(p, RunnerOptions{})
	r.Run(context.Background())
	r.Stop()
	r.Wait()
}

func TestNormalizeConfig_AppliesDefaults(t *testing.T) {
	got := normalizeConfig(Config{})
	if got.Interval != 5*time.Second {
		t.Fatalf("Interval default = %s, want 5s", got.Interval)
	}
	if got.MissesAllowed != 3 {
		t.Fatalf("MissesAllowed default = %d, want 3", got.MissesAllowed)
	}
	if got.InitialGrace != 60*time.Second {
		t.Fatalf("InitialGrace default = %s, want 60s", got.InitialGrace)
	}
}

// TestRunner_ClosesWhenPeerNeverSignals exercita o loop real com relogio curto.
func TestRunner_ClosesWhenPeerNeverSignals(t *testing.T) {
	startedAt := time.Now().UTC()
	cfg := Config{Interval: 20 * time.Millisecond, MissesAllowed: 3, InitialGrace: 60 * time.Millisecond, CloseWithoutPeerSignal: true}
	p := NewPeer(cfg, startedAt)

	lost := make(chan string, 1)
	ticks := 0
	r := NewRunner(p, RunnerOptions{
		OnTick:     func(context.Context) { ticks++ },
		OnPeerLost: func(reason string) { lost <- reason },
		Logf:       func(string) {},
	})

	go r.Run(context.Background())
	select {
	case reason := <-lost:
		if reason != "peer-timeout" {
			t.Fatalf("reason = %q, want peer-timeout", reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("runner nao encerrou apos a grace inicial")
	}
	if ticks < 0 {
		t.Fatalf("ticks deveria ser lido pelo loop")
	}
}

// TestRunner_KeepsAliveWhilePeerSignals valida que, com o peer respondendo, o
// loop segue rodando e o deadline segue estendendo.
func TestRunner_KeepsAliveWhilePeerSignals(t *testing.T) {
	cfg := Config{Interval: 20 * time.Millisecond, MissesAllowed: 3, InitialGrace: 60 * time.Millisecond}
	p := NewPeer(cfg, time.Now().UTC())

	tickCh := make(chan struct{}, 8)
	r := NewRunner(p, RunnerOptions{
		OnTick:     func(ctx context.Context) { tickCh <- struct{}{} },
		OnPeerLost: func(reason string) {},
		Logf:       func(string) {},
	})

	done := make(chan struct{})
	go func() { r.Run(context.Background()); close(done) }()

	// Simula o peer respondendo a cada tick, antes da janela fechar.
	for i := 0; i < 6; i++ {
		select {
		case <-tickCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("runner nao produziu ticks")
		}
		p.NotePeerSignal(time.Now())
	}

	r.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("runner nao encerrou apos Stop")
	}

	if !p.PeerSeen() {
		t.Fatalf("esperava peerSeen=true")
	}
	if got := p.Deadline(time.Now(), time.Minute); got.IsZero() {
		t.Fatalf("com peer vivo o deadline deveria ser estendido")
	}
}

// TestRunner_RunTwiceDoesNotStartSecondLoop garante que apenas um loop roda
// por Runner: a segunda chamada (concorrente) retorna imediatamente.
func TestRunner_RunTwiceDoesNotStartSecondLoop(t *testing.T) {
	cfg := Config{Interval: 50 * time.Millisecond, MissesAllowed: 3, InitialGrace: time.Hour}
	p := NewPeer(cfg, time.Now().UTC())
	ticks := make(chan struct{}, 16)
	r := NewRunner(p, RunnerOptions{OnTick: func(context.Context) { ticks <- struct{}{} }})

	done1 := make(chan struct{})
	done2 := make(chan struct{})
	go func() { r.Run(context.Background()); close(done1) }()

	// Garante que o loop vencedor ja esta rodando antes da segunda chamada,
	// eliminando a corrida com Stop.
	select {
	case <-ticks:
	case <-time.After(2 * time.Second):
		t.Fatalf("primeiro loop nao produziu ticks")
	}

	go func() { r.Run(context.Background()); close(done2) }()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatalf("segundo Run deveria retornar imediatamente")
	}

	r.Stop()
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatalf("primeiro loop nao encerrou")
	}
	close(ticks)
}

func TestRunner_NilSafe(t *testing.T) {
	var r *Runner
	r.Run(context.Background())
	r.Stop()
	r.Wait()
	NewRunner(nil, RunnerOptions{}).Run(context.Background())
}
