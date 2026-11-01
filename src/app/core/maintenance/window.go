// Package maintenance implementa a janela de manutenção (maintenance window)
// do agente: um período recorrente (dias da semana + hora de início +
// duração) durante o qual o servidor pode executar ações disruptivas
// (reboot/shutdown/patch) sem interromper o usuário.
//
// A janela pode cruzar a meia-noite (start + duração > 24h); Contains e
// NextStart tratam esse caso explicitamente.
//
// Fuso horário: quando a configuração traz timeZone (ID IANA) e StartLocal
// ("HH:mm"), a janela é avaliada em HORA LOCAL dessa zona — os dias da semana
// e o horário de início são interpretados no calendário/relógio local, e o
// início é reconstruído com time.Date na Location (o que cobre mudanças de
// horário de verão automaticamente). Sem timeZone válida e/ou sem StartLocal
// válido vale o caminho legado: StartUTC é interpretado como UTC.
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
	// Vazio = todos os dias. Com Location != nil o dia considerado é o dia
	// LOCAL da Location (e não o dia UTC).
	Days []int
	// StartUTC é a hora de início no formato "HH:mm" interpretada como UTC.
	// É o caminho LEGADO, mantido para compatibilidade: só é usado quando
	// Location é nil ou StartLocal não é um "HH:mm" válido.
	StartUTC string
	// StartLocal é o início em "HH:mm" na hora LOCAL de Location. Ignorado
	// quando Location é nil.
	StartLocal string
	// Location é a zona IANA em que a janela é avaliada (nil = UTC/legado).
	Location *time.Location
	// DurationMinutes é a duração da janela em minutos. Default 60.
	DurationMinutes int
}

// ParseClock decodifica um horário "HH:mm" para minutos desde a meia-noite.
// Formato inválido/vazio → ok=false (config quebrada: a janela nunca fica
// ativa no caminho tolerante).
func ParseClock(s string) (minutes int, ok bool) {
	s = strings.TrimSpace(s)
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

// dayAllowed informa se o dia da semana de t está habilitado na janela.
// t deve ser passado já na zona de avaliação da janela (local quando há
// Location): o dia da semana considerado é o do calendário local.
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
	return ParseClock(w.StartUTC)
}

// usesLocalClock informa se a janela deve ser avaliada em hora local: exige
// Location carregada E StartLocal válido. Caso contrário vale o caminho
// legado (StartUTC em UTC).
func (w Window) usesLocalClock() bool {
	if w.Location == nil {
		return false
	}
	_, ok := ParseClock(w.StartLocal)
	return ok
}

// evalBase converte o instante de referência para a zona em que a janela é
// avaliada: a Location configurada (hora local) ou UTC (legado).
func (w Window) evalBase(t time.Time) time.Time {
	if w.usesLocalClock() {
		return t.In(w.Location)
	}
	return t.UTC()
}

// startAt devolve o INÍCIO da janela no dia civil de day (o dia já deve estar
// na zona de avaliação). Com Location + StartLocal válidos monta o início em
// hora local da Location (DST-aware, pois time.Date resolve as transições);
// caso contrário interpreta StartUTC como UTC (legado).
// ok=false quando o horário de início configurado é inválido.
func (w Window) startAt(day time.Time) (time.Time, bool) {
	zone := time.UTC
	var minutes int
	var ok bool
	if w.usesLocalClock() {
		zone = w.Location
		minutes, ok = ParseClock(w.StartLocal)
	} else {
		minutes, ok = w.parseStartUTC()
	}
	if !ok {
		return time.Time{}, false
	}
	d := day.In(zone)
	return time.Date(d.Year(), d.Month(), d.Day(), minutes/60, minutes%60, 0, 0, zone), true
}

// duration devolve a duração normalizada (<=0 → default 60).
func (w Window) duration() time.Duration {
	if w.DurationMinutes <= 0 {
		return 60 * time.Minute
	}
	return time.Duration(w.DurationMinutes) * time.Minute
}

// Contains informa se t (qualquer fuso — a comparação é de instantes) está
// DENTRO da janela. Janela desligada (Enabled=false) ou horário de início
// inválido → false.
//
// O dia da semana e o horário de início são avaliados na zona da janela:
// hora LOCAL de Location quando configurada, UTC no caminho legado.
//
// A janela cruza a meia-noite quando start+duração passa de 24h: para cobrir
// esse caso, Contains também considera uma janela iniciada no dia ANTERIOR
// (mesmo dia da semana permitido) que ainda esteja em curso.
func (w Window) Contains(t time.Time) bool {
	if !w.Enabled {
		return false
	}
	base := w.evalBase(t)
	dur := w.duration()
	// Offset 0: janela que começa hoje. Offset -1: janela que começou ontem
	// e cruza a meia-noite para hoje.
	for _, offset := range []int{0, -1} {
		day := base.AddDate(0, 0, offset)
		if !w.dayAllowed(day) {
			continue
		}
		start, ok := w.startAt(day)
		if !ok {
			return false
		}
		if !t.Before(start) && t.Before(start.Add(dur)) {
			return true
		}
	}
	return false
}

// NextStart devolve o INÍCIO da próxima janela estritamente depois de t.
// Considera os days-of-week (varre até 8 dias à frente, cobrindo qualquer
// combinação de dias habilitados), com dias/hora na zona da janela (local de
// Location ou UTC no legado). Horário inválido ou Enabled=false → zero time
// (sem próxima janela agendável).
func (w Window) NextStart(t time.Time) time.Time {
	if !w.Enabled {
		return time.Time{}
	}
	base := w.evalBase(t)
	for d := 0; d <= 8; d++ {
		day := base.AddDate(0, 0, d)
		if !w.dayAllowed(day) {
			continue
		}
		candidate, ok := w.startAt(day)
		if !ok {
			return time.Time{}
		}
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
