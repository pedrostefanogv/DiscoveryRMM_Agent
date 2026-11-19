package winregistry

import "testing"

// ─────────────────────────────────────────────────────────────────────────────
// REGRESSÃO: BYPASS DO GUARD POR APELIDO DE HIVE
//
// Encontrado em revisão (2026-11-19). O guard casava as regras por PREFIXO
// ANCORADO NA RAIZ do caminho. Como um hive tem apelidos estruturais, a mesma
// chave era alcançável por outro caminho que não casava com nenhuma regra:
//
//   HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Run   -> bloqueado
//   HKU\S-1-5-21-...-1001\SOFTWARE\...\CurrentVersion\Run -> PERMITIDO
//
// O segundo caminho é a MESMA chave de autostart do usuário logado — existia na
// máquina de teste com 9 valores. Ou seja: a proteção de escrita que o usuário
// pediu explicitamente (Run/RunOnce só leitura) era contornável por navegação.
//
// Estes testes travam as duas direções: os aliases são bloqueados E o uso
// legítimo continua passando (uma correção que bloqueasse tudo não seria
// correção).
// ─────────────────────────────────────────────────────────────────────────────

// TestEvaluate_AliasHivesCannotBypassWriteBlocks cobre o bypass em si.
func TestEvaluate_AliasHivesCannotBypassWriteBlocks(t *testing.T) {
	const sid = "S-1-5-21-907735816-3234815851-748069289-1001"

	cases := []struct {
		name string
		hive Hive
		path string
	}{
		{"Run via HKU/SID", HiveUsers, sid + "\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run"},
		{"RunOnce via HKU/SID", HiveUsers, sid + "\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\RunOnce"},
		{"subchave de Run via HKU/SID", HiveUsers, sid + "\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run\\App"},
		{"Winlogon via HKU/SID", HiveUsers, sid + "\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Winlogon"},
		{"Defender via HKU/SID", HiveUsers, sid + "\\SOFTWARE\\Microsoft\\Windows Defender"},
		{"Policies via HKU/SID", HiveUsers, sid + "\\SOFTWARE\\Policies"},
		{"IFEO via HKU/SID", HiveUsers, sid + "\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Image File Execution Options"},
		{"Run via .DEFAULT", HiveUsers, ".DEFAULT\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run"},
		{"Run via HKU/SID_Classes", HiveUsers, sid + "_Classes\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []Op{OpWrite, OpDelete} {
				if got := Evaluate(tc.hive, tc.path, op); got.Allowed {
					t.Fatalf("BYPASS: op=%s em %s\\%s foi PERMITIDA (regra vazia)", op, tc.hive, tc.path)
				}
			}
		})
	}
}

// TestEvaluate_AliasHivesStillAllowReading confirma que a correção NÃO tornou os
// apelidos somente-bloqueio: a leitura de Run via HKU continua permitida, que é
// exatamente a decisão de produto (diagnóstico de autostart).
func TestEvaluate_AliasHivesStillAllowReading(t *testing.T) {
	const sid = "S-1-5-21-907735816-3234815851-748069289-1001"
	reads := []string{
		sid + "\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run",
		sid + "\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\RunOnce",
		".DEFAULT\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Run",
		sid + "\\SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion\\Winlogon",
	}
	for _, p := range reads {
		if got := Evaluate(HiveUsers, p, OpRead); !got.Allowed {
			t.Errorf("LEITURA de %q deveria ser permitida (diagnóstico); recusada por %s", p, got.Rule)
		}
	}
}

// TestEvaluate_LegitimateUseUnaffected é a outra face da correção: o uso normal
// do operador não pode ser bloqueado por causa do casamento de apelidos.
func TestEvaluate_LegitimateUseUnaffected(t *testing.T) {
	const sid = "S-1-5-21-907735816-3234815851-748069289-1001"
	allowed := []struct {
		hive Hive
		path string
		op   Op
	}{
		{HiveUsers, sid + "\\SOFTWARE\\MinhaEmpresa", OpWrite},
		{HiveUsers, sid + "\\SOFTWARE\\MinhaEmpresa\\App", OpDelete},
		{HiveUsers, ".DEFAULT\\SOFTWARE\\MinhaEmpresa", OpWrite},
		{HiveUsers, sid + "\\SOFTWARE\\Classes\\.txt\\OpenWithProgids", OpWrite},
		{HiveUsers, sid + "_Classes\\.txt\\OpenWithProgids", OpWrite},
		{HiveClassesRoot, ".txt\\OpenWithProgids", OpWrite},
		{HiveLocalMachine, "SOFTWARE\\MinhaEmpresa", OpWrite},
		{HiveLocalMachine, "SOFTWARE\\Classes\\MinhaApp\\CLSID", OpWrite},
	}

	for _, tc := range allowed {
		if got := Evaluate(tc.hive, tc.path, tc.op); !got.Allowed {
			t.Errorf("FALSO POSITIVO: op=%s em %s\\%s recusada por %s: %s",
				tc.op, tc.hive, tc.path, got.Rule, got.Reason)
		}
	}
}

// TestEffectiveRoots cobre a normalização de apelidos diretamente.
func TestEffectiveRoots(t *testing.T) {
	const sid = "S-1-5-21-907735816-3234815851-748069289-1001"

	cases := []struct {
		name  string
		input string
		want  string // a raiz efetiva que precisa estar entre as devolvidas
	}{
		{"SID simples", sid + "\\SOFTWARE\\X", "SOFTWARE\\X"},
		{"perfil padrão", ".DEFAULT\\SOFTWARE\\X", "SOFTWARE\\X"},
		{"espelho do HKCR", "SOFTWARE\\CLASSES\\X", "X"},
		// A visão _Classes é normalizada em DUAS passadas: a primeira prepende o
		// prefixo de Classes, a segunda o remove. As duas formas são raízes
		// efetivas válidas (o conteúdo é o mesmo), então o teste aceita a
		// presença de qualquer uma — o que importa é que "X" (o alvo real das
		// regras por sufixo) apareça.
		{"SID com _Classes", sid + "_Classes\\X", "X"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roots := effectiveRoots(tc.input)
			found := false
			for _, r := range roots {
				if r == tc.want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("effectiveRoots(%q) = %v; esperava conter %q", tc.input, roots, tc.want)
			}
		})
	}

	// Caminho sem apelido devolve ele mesmo (e nada mais).
	if roots := effectiveRoots("SOFTWARE\\MinhaEmpresa"); len(roots) != 1 {
		t.Errorf("caminho sem apelido = %v; queria só ele mesmo", roots)
	}
}

// TestStripStructuralPrefix cobre a normalização de apelido diretamente.
func TestStripStructuralPrefix(t *testing.T) {
	// Entrada que não casa nenhum prefixo: vazio (sinal de "sem mudança").
	if got := stripStructuralPrefix("SOFTWARE\\X"); got != "" {
		t.Errorf("stripStructuralPrefix sem apelido = %q; queria vazio", got)
	}
	// SID sem separador não tem resto a devolver.
	if got := stripStructuralPrefix("S-1-5-21-1"); got != "" {
		t.Errorf("SID sem separador = %q; queria vazio", got)
	}
	// Espelho do HKCR.
	if got := stripStructuralPrefix("SOFTWARE\\CLASSES\\.txt"); got != ".txt" {
		t.Errorf("strip do espelho HKCR = %q; queria .txt", got)
	}
}

// TestEffectiveRoots_Terminates garante que a normalização termina mesmo com
// entrada patológica (o laço tem limite defensivo de 3 passadas).
func TestEffectiveRoots_Terminates(t *testing.T) {
	// Não deve entrar em pânico nem travar com SIDs encadeados.
	roots := effectiveRoots("S-1-5-21-1\\S-1-5-21-2\\S-1-5-21-3\\SOFTWARE\\X")
	if len(roots) == 0 {
		t.Fatal("effectiveRoots não devolveu nenhuma raiz")
	}
	if len(roots) > 4 {
		t.Errorf("effectiveRoots devolveu %d raízes; o limite defensivo não está funcionando", len(roots))
	}
}
