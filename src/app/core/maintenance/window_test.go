package maintenance

import (
	"testing"
	"time"
)

// utcAt monta um time.UTC com data/hora explícita.
func utcAt(y, mo, d, h, mi int) time.Time {
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, time.UTC)
}

func TestWindowContains(t *testing.T) {
	// Semana de referência: 2024-01-07 é domingo, 2024-01-13 é sábado.
	base := utcAt(2024, 1, 8, 0, 0) // segunda-feira
	if base.Weekday() != time.Monday {
		t.Fatalf("data de referência inválida: %s", base.Weekday())
	}

	cases := []struct {
		name string
		w    Window
		t    time.Time
		want bool
	}{
		{
			name: "janela desligada nunca contém",
			w:    Window{Enabled: false, StartUTC: "02:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 2, 30),
			want: false,
		},
		{
			name: "startUTC inválido nunca contém",
			w:    Window{Enabled: true, StartUTC: "25:99", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 2, 30),
			want: false,
		},
		{
			name: "dentro da janela",
			w:    Window{Enabled: true, StartUTC: "02:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 2, 30),
			want: true,
		},
		{
			name: "instante exato de início",
			w:    Window{Enabled: true, StartUTC: "02:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 2, 0),
			want: true,
		},
		{
			name: "instante exato de fim é fora (meio-aberto)",
			w:    Window{Enabled: true, StartUTC: "02:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 3, 0),
			want: false,
		},
		{
			name: "fora da janela mesmo dia",
			w:    Window{Enabled: true, StartUTC: "02:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 5, 0),
			want: false,
		},
		{
			name: "dias vazios = todos os dias (domingo conta)",
			w:    Window{Enabled: true, StartUTC: "01:00", DurationMinutes: 30},
			t:    utcAt(2024, 1, 7, 1, 10), // domingo
			want: true,
		},
		{
			name: "somente sábados: segunda fora",
			w:    Window{Enabled: true, Days: []int{7}, StartUTC: "01:00", DurationMinutes: 30},
			t:    utcAt(2024, 1, 8, 1, 10), // segunda
			want: false,
		},
		{
			name: "somente sábados: sábado dentro",
			w:    Window{Enabled: true, Days: []int{7}, StartUTC: "01:00", DurationMinutes: 30},
			t:    utcAt(2024, 1, 13, 1, 10), // sábado
			want: true,
		},
		{
			name: "domingo e segunda: domingo dentro",
			w:    Window{Enabled: true, Days: []int{1, 2}, StartUTC: "23:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 7, 23, 5), // domingo
			want: true,
		},
		{
			name: "cruza meia-noite: depois da meia-noite ainda dentro",
			w:    Window{Enabled: true, StartUTC: "23:00", DurationMinutes: 180},
			t:    utcAt(2024, 1, 9, 0, 30), // 00:30 do dia seguinte à 23:00 de terça
			want: true,
		},
		{
			name: "cruza meia-noite: passou do fim",
			w:    Window{Enabled: true, StartUTC: "23:00", DurationMinutes: 120},
			t:    utcAt(2024, 1, 9, 1, 1),
			want: false,
		},
		{
			name: "cruza meia-noite: dia anterior permitido é exigido",
			w:    Window{Enabled: true, Days: []int{7}, StartUTC: "23:00", DurationMinutes: 180}, // só sábado
			t:    utcAt(2024, 1, 14, 0, 30),                                                      // domingo 00:30 (janela do sábado 23:00)
			want: true,
		},
		{
			name: "dias inválidos são ignorados, lista restante vale",
			w:    Window{Enabled: true, Days: []int{0, 2, 9}, StartUTC: "01:00", DurationMinutes: 30},
			t:    utcAt(2024, 1, 8, 1, 5), // segunda=2
			want: true,
		},
		{
			name: "duração <=0 usa default 60",
			w:    Window{Enabled: true, StartUTC: "04:00"},
			t:    utcAt(2024, 1, 8, 4, 59),
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.Contains(tc.t); got != tc.want {
				t.Fatalf("Contains(%s)=%v, want %v", tc.t, got, tc.want)
			}
		})
	}
	_ = base
}

func TestWindowNextStart(t *testing.T) {
	cases := []struct {
		name string
		w    Window
		t    time.Time
		want time.Time
	}{
		{
			name: "janela desligada → zero",
			w:    Window{Enabled: false, StartUTC: "02:00"},
			t:    utcAt(2024, 1, 8, 1, 0),
			want: time.Time{},
		},
		{
			name: "startUTC inválido → zero",
			w:    Window{Enabled: true, StartUTC: "abc"},
			t:    utcAt(2024, 1, 8, 1, 0),
			want: time.Time{},
		},
		{
			name: "hoje ainda tem janela",
			w:    Window{Enabled: true, StartUTC: "22:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 10, 0),
			want: utcAt(2024, 1, 8, 22, 0),
		},
		{
			name: "janela de hoje já passou → amanhã",
			w:    Window{Enabled: true, StartUTC: "02:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 3, 0),
			want: utcAt(2024, 1, 9, 2, 0),
		},
		{
			name: "dentro da janela → próxima é o próximo dia permitido",
			w:    Window{Enabled: true, StartUTC: "02:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 8, 2, 30),
			want: utcAt(2024, 1, 9, 2, 0),
		},
		{
			name: "dias da semana: pula dias desabilitados",
			w:    Window{Enabled: true, Days: []int{2}, StartUTC: "05:00", DurationMinutes: 60}, // só segunda
			t:    utcAt(2024, 1, 9, 6, 0),                                                       // terça, após a janela de segunda
			want: utcAt(2024, 1, 15, 5, 0),
		},
		{
			name: "dias da semana: sábado a partir de sexta",
			w:    Window{Enabled: true, Days: []int{7}, StartUTC: "23:00", DurationMinutes: 60},
			t:    utcAt(2024, 1, 12, 12, 0), // sexta
			want: utcAt(2024, 1, 13, 23, 0),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.w.NextStart(tc.t)
			if !got.Equal(tc.want) {
				t.Fatalf("NextStart(%s)=%s, want %s", tc.t, got, tc.want)
			}
		})
	}
}

func TestWindowEvaluate(t *testing.T) {
	w := Window{Enabled: true, StartUTC: "02:00", DurationMinutes: 60}
	if d := w.Evaluate(utcAt(2024, 1, 8, 2, 30)); d.Deferred {
		t.Fatalf("dentro da janela não deveria adiar: %s", d)
	}
	if d := w.Evaluate(utcAt(2024, 1, 8, 5, 0)); !d.Deferred || !d.NextRunAt.Equal(utcAt(2024, 1, 9, 2, 0)) {
		t.Fatalf("fora da janela deveria adiar com NextStart de amanhã: %s", d)
	}
	off := Window{Enabled: false, StartUTC: "02:00"}
	if d := off.Evaluate(utcAt(2024, 1, 8, 5, 0)); d.Deferred {
		t.Fatalf("janela desligada não deveria adiar: %s", d)
	}
}
