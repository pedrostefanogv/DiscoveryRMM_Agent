package windowsupdate

import (
	"testing"
	"time"
)

// Regressao do bug real (maquina 019faa6f): as linhas do Win32_QuickFixEngineering
// devem produzir titulos reais, chaves canonicas e datas parseadas. A extracao
// via COM e testada indiretamente aqui: a mesma estrutura de linha que o WMI
// devolve e alimentada na montagem dos Updates.
func TestBuildsInstalledUpdatesFromRows(t *testing.T) {
	rows := []map[string]any{
		{
			"HotFixID":    "KB5127690",
			"Caption":     "http://support.microsoft.com/?kbid=5127690",
			"Description": "Update",
			"InstalledOn": "2026-10-16 21:09:57",
		},
		{
			"HotFixID":    "5127846",
			"Caption":     "",
			"Description": "",
			"InstalledOn": "16/10/2026",
		},
		{
			"HotFixID": "KB5127690", // duplicada (QFE as vezes repete)
		},
		{
			"HotFixID": "lixo",
		},
	}
	updates := buildsInstalledUpdatesFromRows(rows)
	if len(updates) != 2 {
		t.Fatalf("len=%d want 2 (%+v)", len(updates), updates)
	}
	if updates[0].UpdateKey != "kb:KB5127690" {
		t.Fatalf("UpdateKey=%q", updates[0].UpdateKey)
	}
	if updates[0].Title != "Atualizacao KB5127690" {
		t.Fatalf("titulo da KB com caption URL=%q", updates[0].Title)
	}
	if updates[0].Source != SourceQFE || !updates[0].IsInstalled {
		t.Fatalf("metadados invalidos: %+v", updates[0])
	}
	if updates[0].InstalledAt == nil {
		t.Fatal("InstalledOn nao parseada")
	}
	if updates[1].InstalledAt == nil {
		t.Fatal("InstalledOn pt-BR nao parseada")
	}
}

func TestParseWMIInstalledOn(t *testing.T) {
	cases := map[string]bool{
		"20261016":                  true,
		"2026-10-16":                true,
		"2026-10-16 21:09:57":       true,
		"16/10/2026":                true,
		"10/16/2026":                true,
		"20261016210957.000000-180": true,
		"2026-10-16T21:09:57Z":      true,
		"":                          false,
		"ontem":                     false,
	}
	for raw, ok := range cases {
		got := parseWMIInstalledOn(raw)
		if ok && got == nil {
			t.Fatalf("parseWMIInstalledOn(%q) deveria parsear", raw)
		}
		if !ok && got != nil {
			t.Fatalf("parseWMIInstalledOn(%q) nao deveria parsear (%v)", raw, got)
		}
	}
	parsed := parseWMIInstalledOn("2026-10-16 21:09:57")
	if parsed == nil || !parsed.Equal(time.Date(2026, 10, 16, 21, 9, 57, 0, time.UTC)) {
		t.Fatalf("data incorreta: %v", parsed)
	}
}

// withShortDate injeta o formato curto de data do SO (LOCALE_SSHORTDATE):
// dayFirst=true em pt-BR (dd/MM/yyyy), false em en-US (M/d/yyyy).
func withShortDate(t *testing.T, dayFirst, known bool) {
	t.Helper()
	previous := windowsShortDateDayFirst
	windowsShortDateDayFirst = func() (bool, bool) { return dayFirst, known }
	t.Cleanup(func() { windowsShortDateDayFirst = previous })
}

// Data com barra casa nos DOIS layouts (dd/mm e mm/dd): quem decide é o locale
// do SO. Regressão: com heurística temporal, "05/03/2026" em pt-BR virava
// 3 de maio (mês/dia) — errado para uma máquina brasileira.
func TestParseWMIInstalledOnAmbiguousDates(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)

	cases := []struct {
		name     string
		raw      string
		now      time.Time
		dayFirst bool
		known    bool
		want     time.Time
	}{
		{
			name: "pt-BR escolhe dia/mes",
			raw:  "05/03/2026",
			now:  now,
			dayFirst: true, known: true,
			want: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "en-US escolhe mes/dia",
			raw:  "05/03/2026",
			now:  now,
			dayFirst: false, known: true,
			want: time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
		},
		{
			// pt-BR: 05/12/2026 = 5 de dezembro, que está no FUTURO. Data de
			// instalação não pode ser futura: cai para a outra interpretação.
			name: "pt-BR com data futura cai para a outra leitura",
			raw:  "05/12/2026",
			now:  now,
			dayFirst: true, known: true,
			want: time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		},
		{
			// Locale desconhecido: heurística antiga (mais próxima de agora).
			name:     "locale desconhecido usa heuristica temporal",
			raw:      "05/03/2026",
			now:      now,
			dayFirst: false, known: false,
			want: time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC),
		},
		{
			// Dia 13 não existe como mês: só o layout dd/mm casa.
			name: "somente dia/mes",
			raw:  "13/03/2026",
			now:  now,
			dayFirst: true, known: true,
			want: time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC),
		},
		{
			// 31/12 não existe como mês/dia; só dd/mm casa.
			name: "31/12/2025",
			raw:  "31/12/2025",
			now:  now,
			dayFirst: true, known: true,
			want: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
		},
		{
			// Relógio errado: todas as interpretações no futuro => fica a mais
			// próxima de agora (determinismo preservado).
			name: "todas no futuro",
			raw:  "05/03/2027",
			now:  now,
			dayFirst: true, known: true,
			want: time.Date(2027, 3, 5, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withShortDate(t, tc.dayFirst, tc.known)
			got := parseWMIInstalledOnAt(tc.raw, tc.now)
			if got == nil {
				t.Fatalf("parseWMIInstalledOnAt(%q) nao parseou", tc.raw)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("parseWMIInstalledOnAt(%q)=%s want %s", tc.raw, got, tc.want)
			}
		})
	}
}

// Data única (sem ambiguidade) no FUTURO é descartada: instalação futura não
// existe, e agora a recoleta sobrescreve a data conhecida.
func TestParseWMIInstalledOnFutureIsRejected(t *testing.T) {
	withShortDate(t, true, true)
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	if got := parseWMIInstalledOnAt("31/12/2026", now); got != nil {
		t.Fatalf("data futura deveria ser descartada, veio %s", got)
	}
	if got := parseWMIInstalledOnAt("31/12/2025", now); got == nil {
		t.Fatal("data passada deveria continuar valendo")
	}
}

// A leitura do locale não pode derrubar o parse: sem Windows respondendo, cai
// na heurística temporal em vez de perder a data.
func TestParseWMIInstalledOnWithoutLocaleStillParses(t *testing.T) {
	withShortDate(t, false, false)
	got := parseWMIInstalledOnAt("16/10/2026", time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC))
	if got == nil || got.Month() != time.October || got.Day() != 16 {
		t.Fatalf("parse sem locale=%v want 2026-10-16", got)
	}
}
