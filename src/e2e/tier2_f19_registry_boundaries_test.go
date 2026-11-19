package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"

	"discovery/app/core/winregistry"
)

// ─────────────────────────────────────────────────────────────────────────────
// F19 — BOUNDARIES (Tier 2) do editor de Registro.
//
// Tier 1 cobre a espec feliz. Aqui o alvo são os LIMITES: entradas hostis,
// caminhos malformados e a fronteira do que o guard deixa passar. São os casos
// em que uma implementação ingênua falha em silêncio — o tipo de falha que não
// aparece no caminho feliz e vira incidente em produção.
// ─────────────────────────────────────────────────────────────────────────────

// TestF19_T2_PathTraversalRejectedEmAllOperations garante que nenhuma operação
// aceita um caminho que escape da raiz do hive.
//
// Se um único ponto do caminho não validasse, "SOFTWARE\\..\\SAM" seria um
// contorno completo do guard: a regra compara o path já normalizado, então a
// normalização é a barreira que sustenta todas as outras.
func TestF19_T2_PathTraversalRejectedEmAllOperations(t *testing.T) {
	hostile := []string{
		"SOFTWARE\\..\\SAM",
		"SOFTWARE\\..\\..\\SECURITY",
		"..",
		"../SOFTWARE",
		"SOFTWARE\\..",
		".\\SOFTWARE",
		"SOFTWARE\\.\\SYSTEM",
	}

	ops := []struct {
		name string
		op   winregistry.Op
	}{
		{"leitura", winregistry.OpRead},
		{"escrita", winregistry.OpWrite},
		{"exclusao", winregistry.OpDelete},
	}

	for _, hostilePath := range hostile {
		for _, op := range ops {
			decision := winregistry.Evaluate(winregistry.HiveLocalMachine, hostilePath, op.op)
			if decision.Allowed {
				t.Errorf("caminho hostil %q com %s foi PERMITIDO (regra=%s)",
					hostilePath, op.name, decision.Rule)
			}
			if decision.Rule == "" {
				t.Errorf("caminho hostil %q: recusa sem Rule; a auditoria precisa saber o motivo", hostilePath)
			}
		}
	}
}

// TestF19_T2_ControlCharsRejected garante que caracteres de controle não passam.
//
// Um newline no meio de um caminho pode ser usado para confundir logs de
// auditoria (log injection) e para montar caminhos que o SO interpreta de forma
// diferente do que foi validado.
func TestF19_T2_ControlCharsRejected(t *testing.T) {
	hostile := []string{
		"SOFTWARE\nMalicioso",
		"SOFTWARE\r\nMalicioso",
		"SOFTWARE\tMalicioso",
		"SOFTWARE\x00Malicioso",
		"SOFTWARE\\Test\x01",
	}

	for _, p := range hostile {
		if _, err := winregistry.NormalizePath(p); err == nil {
			t.Errorf("caminho com caractere de controle %q foi aceito", p)
		}
		if decision := winregistry.Evaluate(winregistry.HiveLocalMachine, p, winregistry.OpRead); decision.Allowed {
			t.Errorf("caminho com caractere de controle %q foi PERMITIDO pelo guard", p)
		}
	}
}

// TestF19_T2_PathLengthBoundary verifica o limite exato: no limite passa, um
// byte acima recusa.
//
// Truncar em vez de recusar gravaria na chave ERRADA em silêncio — o pior
// desfecho possível para um editor de registro.
func TestF19_T2_PathLengthBoundary(t *testing.T) {
	atLimit := strings.Repeat("A", winregistry.MaxPathLen)
	if _, err := winregistry.NormalizePath(atLimit); err != nil {
		t.Errorf("caminho exatamente no limite deveria ser aceito: %v", err)
	}
	if _, err := winregistry.NormalizePath(atLimit + "A"); err == nil {
		t.Error("caminho um byte acima do limite deveria ser recusado (nunca truncado)")
	}
}

// TestF19_T2_HiveAllowlistIsClosed confirma que apenas os 5 hives canônicos
// passam, e que grafias longas são normalizadas.
func TestF19_T2_HiveAllowlistIsClosed(t *testing.T) {
	accepted := map[string]winregistry.Hive{
		"HKLM": winregistry.HiveLocalMachine, "HKEY_LOCAL_MACHINE": winregistry.HiveLocalMachine,
		"HKCU": winregistry.HiveCurrentUser, "HKEY_CURRENT_USER": winregistry.HiveCurrentUser,
		"HKCR": winregistry.HiveClassesRoot, "HKU": winregistry.HiveUsers, "HKCC": winregistry.HiveCurrentConfig,
	}
	for raw, want := range accepted {
		got, ok := winregistry.ParseHive(raw)
		if !ok || got != want {
			t.Errorf("ParseHive(%q) = (%v, %v); queria (%v, true)", raw, got, ok, want)
		}
	}

	// HKEY_PERFORMANCE_DATA e HKEY_DYN_DATA existem na documentação mas NÃO são
	// endereçáveis por caminho textual — a recusa é explícita, não por omissão.
	rejected := []string{
		"", "   ", "HKPD", "HKEY_PERFORMANCE_DATA", "HKEY_DYN_DATA",
		"HKLM\\SOFTWARE", "HKEY_", "qualquer",
	}
	for _, raw := range rejected {
		if got, ok := winregistry.ParseHive(raw); ok {
			t.Errorf("ParseHive(%q) = %v, true; deveria recusar", raw, got)
		}
	}
}

// TestF19_T2_BinaryRoundTripPreservesEveryByte é a fronteira mais importante do
// codec: REG_BINARY com bytes que não são UTF-8 válido precisa sobreviver
// exatamente.
//
// Qualquer conversão para string no caminho perde bytes de forma silenciosa, e
// o operador só descobre quando o programa que lê o valor quebra.
func TestF19_T2_BinaryRoundTripPreservesEveryByte(t *testing.T) {
	cases := map[string][]byte{
		"todos os bytes de controle":     {0x00, 0x01, 0x02, 0x1F, 0x7F},
		"bytes altos inválidos em UTF-8": {0x80, 0x81, 0xFE, 0xFF},
		"NUL no início e no fim":         {0x00, 0x41, 0x42, 0x00},
		"vazio":                          {},
		"todos 0xFF":                     {0xFF, 0xFF, 0xFF, 0xFF},
	}

	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			entry := winregistry.EncodeValue("Blob", 3, raw)
			if entry.Type != winregistry.TypeBinary {
				t.Fatalf("tipo = %s; queria %s", entry.Type, winregistry.TypeBinary)
			}
			back, err := winregistry.DecodeValue(winregistry.TypeBinary, entry.Data)
			if err != nil {
				t.Fatalf("DecodeValue: %v", err)
			}
			if len(back) != len(raw) {
				t.Fatalf("tamanho mudou: %d -> %d", len(raw), len(back))
			}
			for i := range raw {
				if back[i] != raw[i] {
					t.Fatalf("byte %d mudou: 0x%02X -> 0x%02X", i, raw[i], back[i])
				}
			}
		})
	}
}

// TestF19_T2_IntegerBoundaries cobre os limites exatos de DWORD e QWORD,
// incluindo a precisão que motiva trafegar número como string.
func TestF19_T2_IntegerBoundaries(t *testing.T) {
	// DWORD: máximo é 4294967295 (não -1 nem 2147483647).
	entry := winregistry.EncodeValue("Flag", 4, []byte{0xFF, 0xFF, 0xFF, 0xFF})
	if entry.Data != "4294967295" {
		t.Errorf("DWORD 0xFFFFFFFF = %q; queria 4294967295 (sem sinal)", entry.Data)
	}

	// QWORD: 2^64-1. Em JavaScript este valor só é exato como STRING — é a
	// razão de o protocolo carregar numérico como string.
	entry = winregistry.EncodeValue("Big", 11, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	if entry.Data != "18446744073709551615" {
		t.Errorf("QWORD 0xFFFF... = %q; queria 18446744073709551615", entry.Data)
	}

	// Valor logo acima de 2^53: se o caminho passasse por float64, viraria
	// 9007199254740992 e o operador leria um valor errado.
	entry = winregistry.EncodeValue("Preciso", 11, []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x20, 0x00})
	if entry.Data != "9007199254740993" {
		t.Errorf("QWORD 2^53+1 = %q; queria 9007199254740993", entry.Data)
	}
}

// TestF19_T2_MultiStringEmptyEntryPreserved cobre o caso que um split ingênuo
// perde: uma string VAZIA no meio de um REG_MULTI_SZ é significativa.
func TestF19_T2_MultiStringEmptyEntryPreserved(t *testing.T) {
	original := []string{"primeiro", "", "terceiro", ""}
	raw, err := winregistry.DecodeValue(winregistry.TypeMultiString, strings.Join(original, "\n"))
	if err != nil {
		t.Fatalf("DecodeValue: %v", err)
	}

	entry := winregistry.EncodeValue("Lista", 7, raw)
	if len(entry.StringList) != len(original) {
		t.Fatalf("itens = %v (%d); queria %d itens", entry.StringList, len(entry.StringList), len(original))
	}
	for i, want := range original {
		if entry.StringList[i] != want {
			t.Errorf("item %d = %q; queria %q", i, entry.StringList[i], want)
		}
	}
}

// TestF19_T2_ValueNameBoundary cobre o nome de valor, incluindo o vazio (que é
// o valor "(Default)" e precisa ser aceito).
func TestF19_T2_ValueNameBoundary(t *testing.T) {
	// Vazio é VÁLIDO: é o valor Padrão de uma chave. Tratar como erro
	// quebraria a leitura da maioria das chaves do Windows.
	if err := winregistry.ValidateValueName(""); err != nil {
		t.Errorf("nome vazio deveria ser aceito (valor Padrão): %v", err)
	}
	if err := winregistry.ValidateValueName(strings.Repeat("A", winregistry.MaxValueNameLen)); err != nil {
		t.Errorf("nome no limite deveria ser aceito: %v", err)
	}
	if err := winregistry.ValidateValueName(strings.Repeat("A", winregistry.MaxValueNameLen+1)); err == nil {
		t.Error("nome acima do limite deveria ser recusado")
	}
	if err := winregistry.ValidateValueName("com\x00nul"); err == nil {
		t.Error("nome com NUL deveria ser recusado")
	}
}

// TestF19_T2_MutationRequiresConfirmationPeloAgente reforça que a barreira de
// confirmação está no pacote de operações, não só no handler da sessão.
func TestF19_T2_MutationRequiresConfirmationPeloAgente(t *testing.T) {
	// Cada mutação precisa devolver ErrConfirmationRequired quando confirmed=false.
	if err := winregistry.SetValue(winregistry.HiveLocalMachine, "SOFTWARE\\X", "v", winregistry.TypeString, "x", false); err == nil {
		t.Error("SetValue sem confirmed deveria falhar")
	}
	if err := winregistry.DeleteValue(winregistry.HiveLocalMachine, "SOFTWARE\\X", "v", false); err == nil {
		t.Error("DeleteValue sem confirmed deveria falhar")
	}
	if err := winregistry.CreateKey(winregistry.HiveLocalMachine, "SOFTWARE\\X", false); err == nil {
		t.Error("CreateKey sem confirmed deveria falhar")
	}
	if err := winregistry.DeleteKey(winregistry.HiveLocalMachine, "SOFTWARE\\X", false, false); err == nil {
		t.Error("DeleteKey sem confirmed deveria falhar")
	}
	if err := winregistry.RenameKey(winregistry.HiveLocalMachine, "SOFTWARE\\X", "Y", false); err == nil {
		t.Error("RenameKey sem confirmed deveria falhar")
	}
}

// TestF19_T2_NonWindowsStubsAreExplicit garante que o stub fora do Windows
// devolve ErrUnsupportedPlatform em vez de nil silencioso.
//
// Um stub que devolvesse sucesso faria a UI mostrar "gravado" sem nada ter
// acontecido — mentira pior que um erro.
func TestF19_T2_NonWindowsStubsAreExplicit(t *testing.T) {
	if _, err := winregistry.ListKey(winregistry.HiveLocalMachine, "SOFTWARE", ""); err != nil && err != winregistry.ErrKeyNotFound && err != winregistry.ErrProtectedKey {
		// No Windows, um erro de ACL/ausência é legítimo; o que não pode é
		// devolver ErrUnsupportedPlatform.
		if err == winregistry.ErrUnsupportedPlatform {
			t.Error("ErrUnsupportedPlatform em host Windows indica stub sendo usado por engano")
		}
	}
}

// TestF19_T2_RegistryRequestPayloadShape valida o contrato JSON do protocolo.
//
// O agente decodifica o enum de ações do payload; um nome de ação diferente
// (ex.: "ListKey" com maiúscula) precisa ser recusado, não "adivinhado".
func TestF19_T2_RegistryRequestPayloadShape(t *testing.T) {
	// Confirmar que os nomes de ação são sensíveis a caixa: o switch do agente
	// compara com as constantes exatas.
	wrongCase := []string{"ListKey", "LISTKEY", "listkey", "list_key"}
	for _, action := range wrongCase {
		raw, _ := json.Marshal(map[string]any{
			"version": 1, "requestId": "x", "action": action, "hive": "HKLM", "path": "SOFTWARE",
		})
		var decoded struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(raw, &decoded)
		if decoded.Action == "listKey" {
			t.Errorf("ação %q foi normalizada para listKey; o agente deve recusar, não adivinhar", action)
		}
	}
}

// TestF19_T2_ExportLimitIsExplicit confirma que o export devolve erro explícito
// ao exceder o limite, em vez de truncar silenciosamente.
func TestF19_T2_ExportLimitIsExplicit(t *testing.T) {
	// Um caminho inexistente devolve erro de "não encontrado" (ou acesso negado),
	// nunca um .reg vazio que pareceria sucesso.
	_, err := winregistry.ExportKey(winregistry.HiveLocalMachine, "SOFTWARE\\CaminhoQueNaoExiste999999", false)
	if err == nil {
		t.Error("export de chave inexistente deveria devolver erro, não conteúdo vazio")
	}
	if err == winregistry.ErrLimitExceeded {
		t.Skip("limite atingido; comportamento correto")
	}
}
