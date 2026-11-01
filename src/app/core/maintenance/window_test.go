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

// brSP carrega America/Sao_Paulo (UTC-3, sem horário de verão desde 2019).
func brSP(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("LoadLocation(America/Sao_Paulo): %v", err)
	}
	return loc
}

func TestWindowContainsLocalTimeZone(t *testing.T) {
	loc := brSP(t)
	// Janela 23:00 local (America/Sao_Paulo, UTC-3), duração 60 min:
	// 2024-06-10 (segunda) 23:00 local == 2024-06-11 02:00 UTC.
	w := Window{Enabled: true, StartLocal: "23:00", Location: loc, DurationMinutes: 60}

	if w.Contains(utcAt(2024, 6, 10, 1, 0)) {
		t.Fatal("01:00 UTC do dia 10 é 22:00 local do dia 9: fora da janela")
	}
	if !w.Contains(utcAt(2024, 6, 11, 2, 30)) {
		t.Fatal("02:30 UTC do dia 11 é 23:30 local do dia 10: deveria estar dentro")
	}
	if w.Contains(utcAt(2024, 6, 11, 3, 0)) {
		t.Fatal("03:00 UTC do dia 11 é 00:00 local do dia 11: fim da janela (meio-aberto)")
	}
	// O mesmo relógio (23:30) em outro instante UTC do dia seguinte não é a janela do dia 10.
	if !w.Contains(utcAt(2024, 6, 12, 2, 30)) {
		t.Fatal("23:30 local do dia 11 (02:30 UTC do dia 12) deveria estar dentro")
	}
	if w.Contains(utcAt(2024, 6, 12, 1, 0)) {
		t.Fatal("01:00 UTC do dia 12 é 22:00 local do dia 11: fora")
	}
}

func TestWindowNextStartLocalTimeZone(t *testing.T) {
	loc := brSP(t)
	w := Window{Enabled: true, StartLocal: "23:00", Location: loc, DurationMinutes: 60}
	// 2024-06-10 12:00 UTC = 09:00 local de segunda. Próximo início:
	// 23:00 local de segunda == 2024-06-11 02:00 UTC.
	got := w.NextStart(utcAt(2024, 6, 10, 12, 0))
	want := utcAt(2024, 6, 11, 2, 0)
	if !got.Equal(want) {
		t.Fatalf("NextStart=%s, want %s", got, want)
	}
	// Já dentro da janela (02:30 UTC do dia 11) → próximo é o dia seguinte local.
	got = w.NextStart(utcAt(2024, 6, 11, 2, 30))
	if want := utcAt(2024, 6, 12, 2, 0); !got.Equal(want) {
		t.Fatalf("NextStart dentro da janela=%s, want %s", got, want)
	}
}

func TestWindowLocalDayOfWeek(t *testing.T) {
	loc := brSP(t)
	// Só segunda-feira (2), 23:00 local (UTC-3), duração 120.
	// 2024-06-10 é segunda: janela 23:00 seg → 01:00 ter local == 02:00–04:00 UTC de terça.
	w := Window{Enabled: true, Days: []int{2}, StartLocal: "23:00", Location: loc, DurationMinutes: 120}
	// 02:30 UTC de terça = 23:30 local de SEGUNDA: dentro (o dia local é que conta,
	// e o dia UTC de 2024-06-11 é terça/3 — o legado negaria).
	if !w.Contains(utcAt(2024, 6, 11, 2, 30)) {
		t.Fatal("23:30 local de segunda deveria estar dentro da janela de segunda")
	}
	// 02:30 UTC de quarta = 23:30 local de TERÇA: fora.
	if w.Contains(utcAt(2024, 6, 12, 2, 30)) {
		t.Fatal("23:30 local de terça não deveria estar na janela de segunda")
	}
	// Próximo início a partir de terça 12:00 UTC = segunda da semana seguinte, 02:00 UTC.
	got := w.NextStart(utcAt(2024, 6, 11, 12, 0))
	if want := utcAt(2024, 6, 18, 2, 0); !got.Equal(want) {
		t.Fatalf("NextStart=%s, want %s", got, want)
	}
}

func TestWindowCrossesMidnightLocal(t *testing.T) {
	loc := brSP(t)
	w := Window{Enabled: true, StartLocal: "23:00", Location: loc, DurationMinutes: 180}
	if !w.Contains(utcAt(2024, 6, 11, 4, 30)) { // 01:30 local do dia 11
		t.Fatal("janela local que cruza a meia-noite deveria conter 01:30 local")
	}
	if w.Contains(utcAt(2024, 6, 11, 5, 1)) { // 02:01 local do dia 11
		t.Fatal("passou do fim da janela local (180 min)")
	}
}

func TestWindowDSTAware(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation(America/New_York): %v", err)
	}
	w := Window{Enabled: true, StartLocal: "23:00", Location: loc, DurationMinutes: 60}
	// 2024-03-09 23:00 EST (UTC-5) == 2024-03-10 04:00 UTC.
	if got, want := w.NextStart(utcAt(2024, 3, 9, 12, 0)), utcAt(2024, 3, 10, 4, 0); !got.Equal(want) {
		t.Fatalf("NextStart antes do DST=%s, want %s", got, want)
	}
	// 2024-03-10 23:00 EDT (UTC-4, após a virada) == 2024-03-11 03:00 UTC.
	if got, want := w.NextStart(utcAt(2024, 3, 10, 12, 0)), utcAt(2024, 3, 11, 3, 0); !got.Equal(want) {
		t.Fatalf("NextStart depois do DST=%s, want %s", got, want)
	}
}

func TestWindowLegacyFallback(t *testing.T) {
	loc := brSP(t)
	// Location nil mesmo com StartLocal: vale StartUTC como UTC (legado).
	w := Window{Enabled: true, StartUTC: "02:00", StartLocal: "23:00", DurationMinutes: 60}
	if !w.Contains(utcAt(2024, 6, 10, 2, 30)) {
		t.Fatal("sem Location o caminho legado (StartUTC em UTC) deveria valer")
	}
	if w.Contains(utcAt(2024, 6, 10, 23, 30)) {
		t.Fatal("sem Location StartLocal deve ser ignorado")
	}
	// Location presente mas StartLocal inválido: cai no legado (StartUTC em UTC).
	w2 := Window{Enabled: true, StartUTC: "02:00", StartLocal: "25:00", Location: loc, DurationMinutes: 60}
	if !w2.Contains(utcAt(2024, 6, 10, 2, 30)) {
		t.Fatal("StartLocal inválido deveria cair no caminho legado")
	}
	// Sem nenhum horário válido a janela nunca fica ativa.
	w3 := Window{Enabled: true, StartLocal: "23:00"} // nem Location nem StartUTC
	if w3.Contains(utcAt(2024, 6, 10, 2, 30)) {
		t.Fatal("sem horário válido a janela não deveria conter nada")
	}
	if got := w3.NextStart(utcAt(2024, 6, 10, 2, 30)); !got.IsZero() {
		t.Fatalf("sem horário válido NextStart deveria ser zero: %s", got)
	}
}
