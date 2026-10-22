// Package maintenance implementa a janela de manutenção (maintenance window)
// do agente: um período recorrente (dias da semana + hora UTC de início +
// duração) durante o qual o servidor pode executar ações disruptivas
// (reboot/shutdown/patch) sem interromper o usuário.
//
// A janela pode cruzar a meia-noite (start + duração > 24h); Contains e
// NextStart tratam esse caso explicitamente. Todos os horários são UTC.
package maintenance

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Window descreve uma janela de manutenção resolvida (defaults já aplicados
// pela configuração do agente).
type Window struct {
	// Enabled é o kill switch: false = nenhuma restrição (comportamento atual).
	Enabled bool
	// Days são os dias da semana em que a janela está ativa, 1=domingo..7=sábado.
	// Vazio = todos os dias.
	Days []int
	// StartUTC é a hora local (UTC) de início no formato "HH:mm".
	StartUTC string
	// DurationMinutes é a duração da janela em minutos. Default 60.
	DurationMinutes int
}

// dayAllowed informa se o dia da semana de t está habilitado na janela.
// Lista vazia (ou inválida) = todos os dias. Dias fora de 1..7 são ignorados.
func (w Window) dayAllowed(t time.Time) bool {
	if len(w.Days) == 0 {
		return true
	}
	// time.Weekday: Sunday=0..Saturday=6 → contrato: 1=domingo..7=sábado.
	dow := int(t.Weekday()) + 1
	for _, d := range w.Days {
		if d == dow {
			return true
		}
	}
	return false
}

// parseStartUTC decodifica StartUTC ("HH:mm") para minutos desde a meia-noite.
// Formato inválido/vazio → ok=false (a janela nunca fica ativa: é o caminho
// tolerante a config quebrada — o servidor manda HH:mm válido).
func (w Window) parseStartUTC() (minutes int, ok bool) {
	s := strings.TrimSpace(w.StartUTC)
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	hh, errH := strconv.Atoi(s[:2])
	mm, errM := strconv.Atoi(s[3:])
	if errH != nil || errM != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// duration devolve a duração normalizada (<=0 → default 60).
func (w Window) duration() time.Duration {
	if w.DurationMinutes <= 0 {
		return 60 * time.Minute
	}
	return time.Duration(w.DurationMinutes) * time.Minute
}

// Contains informa se t (UTC ou não — só os campos de relógio do dia contam)
// está DENTRO da janela. Janela desligada (Enabled=false) ou StartUTC
// inválido → false.
//
// A janela cruza a meia-noite quando start+duração passa de 24h: para cobrir
// esse caso, Contains também considera uma janela iniciada no dia ANTERIOR
// (mesmo dia da semana permitido) que ainda esteja em curso.
func (w Window) Contains(t time.Time) bool {
	if !w.Enabled {
		return false
	}
	startMin, ok := w.parseStartUTC()
	if !ok {
		return false
	}
	dur := w.duration()
	// Offset 0: janela que começa hoje. Offset -1: janela que começou ontem
	// e cruza a meia-noite para hoje.
	for _, offset := range []int{0, -1} {
		day := t.AddDate(0, 0, offset)
		if !w.dayAllowed(day) {
			continue
		}
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location()).
			Add(time.Duration(startMin) * time.Minute)
		if !t.Before(start) && t.Before(start.Add(dur)) {
			return true
		}
	}
	return false
}

// NextStart devolve o INÍCIO da próxima janela estritamente depois de t.
// Considera os days-of-week (varre até 8 dias à frente, cobrindo qualquer
// combinação de dias habilitados). StartUTC inválido ou Enabled=false →
// zero time (sem próxima janela agendável).
func (w Window) NextStart(t time.Time) time.Time {
	if !w.Enabled {
		return time.Time{}
	}
	startMin, ok := w.parseStartUTC()
	if !ok {
		return time.Time{}
	}
	for d := 0; d <= 8; d++ {
		day := t.AddDate(0, 0, d)
		if !w.dayAllowed(day) {
			continue
		}
		candidate := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location()).
			Add(time.Duration(startMin) * time.Minute)
		if candidate.After(t) {
			return candidate
		}
	}
	return time.Time{}
}

// Deferral é o resultado de uma avaliação "pode executar agora?" de uma ação
// disruptiva contra a janela de manutenção.
type Deferral struct {
	// Deferred=true: agora está FORA da janela — aguarde NextRunAt.
	Deferred bool
	// NextRunAt é o início da próxima janela (zero quando Deferred=false).
	NextRunAt time.Time
}

// Evaluate decide se uma ação disruptiva (reboot/patch) pode rodar em now:
// janela desligada ou dentro da janela → executa (Deferred=false); fora da
// janela → deferral com NextStart.
func (w Window) Evaluate(now time.Time) Deferral {
	if !w.Enabled || w.Contains(now) {
		return Deferral{}
	}
	return Deferral{Deferred: true, NextRunAt: w.NextStart(now)}
}

// String é usada em logs (RFC3339 quando há data válida).
func (d Deferral) String() string {
	if !d.Deferred {
		return "immediate"
	}
	return fmt.Sprintf("deferred_to_window nextRunAt=%s", d.NextRunAt.UTC().Format(time.RFC3339))
}
