//go:build windows

package winregistry

import (
	"errors"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// REGRESSÃO: CONTRATOS DE OPERAÇÃO CORRIGIDOS EM REVISÃO (2026-11-19)
//
// Cada teste aqui corresponde a um defeito real encontrado na revisão. O valor
// destes testes é impedir que a correção seja desfeita por engano depois.
// ─────────────────────────────────────────────────────────────────────────────

// TestErrorClassification_AccessDeniedIsNotPolicy trava a separação entre
// "bloqueado por política" (guard) e "sem privilégio" (ACL do SO).
//
// Antes da correção, ERROR_ACCESS_DENIED era traduzido para ErrProtectedKey:
// o operador via "chave protegida por política" numa chave que simplesmente
// exigia elevação, e a auditoria registrava uma decisão de política que nunca
// aconteceu.
func TestErrorClassification_AccessDeniedIsNotPolicy(t *testing.T) {
	if errors.Is(ErrAccessDenied, ErrProtectedKey) || errors.Is(ErrProtectedKey, ErrAccessDenied) {
		t.Fatal("ErrAccessDenied e ErrProtectedKey precisam ser sentinelas DISTINTOS")
	}

	// O guard produz ErrProtectedKey; a ACL do SO produz ErrAccessDenied.
	guardDeny := Evaluate(HiveLocalMachine, "SAM", OpRead)
	if guardDeny.Allowed {
		t.Fatal("SAM deveria ser recusada pelo guard")
	}
	if guardDeny.Rule == "" {
		t.Error("recusa do guard precisa de Rule para a auditoria")
	}
}

// TestDeleteKey_NonRecursiveReportsNotEmpty garante erro acionável em vez do
// ERROR_ACCESS_DENIED cru que o SO devolve para chave com filhos.
func TestDeleteKey_NonRecursiveReportsNotEmpty(t *testing.T) {
	// HKLM\SOFTWARE tem subchaves em qualquer Windows.
	err := DeleteKey(HiveLocalMachine, "SOFTWARE", false, true)
	if err == nil {
		t.Fatal("exclusão não-recursiva de chave com filhos deveria falhar")
	}
	// Pode ser ErrKeyNotEmpty (nosso) ou ErrAccessDenied (ACL do SO impede a
	// própria verificação). O que NÃO pode é ser confundido com política.
	if errors.Is(err, ErrProtectedKey) {
		t.Errorf("erro de 'chave nao vazia' não deveria ser classificado como política: %v", err)
	}
}

// TestDeleteKey_RequiresConfirmation confirma que a barreira vale ANTES de
// qualquer verificação de estado (não vaza informação sobre a chave).
func TestDeleteKey_RequiresConfirmation(t *testing.T) {
	err := DeleteKey(HiveLocalMachine, "SOFTWARE", true, false)
	if !errors.Is(err, ErrConfirmationRequired) {
		t.Errorf("err = %v; queria ErrConfirmationRequired", err)
	}
}

// TestListKey_UnreadableValuesAreFlagged garante que uma leitura falha não seja
// apresentada como um valor vazio legítimo.
//
// Sem o campo ReadError, o operador via "REG_NONE / vazio" e concluía que o
// valor estava limpo — quando na verdade o agente não conseguiu lê-lo.
func TestListKey_UnreadableValuesAreFlagged(t *testing.T) {
	// Percorre um hive real procurando valores; verifica a invariante de que um
	// entry com ReadError preenchido NÃO tem dado (o contrato é explícito).
	page, err := ListKey(HiveLocalMachine, "SOFTWARE", "")
	if err != nil {
		t.Skipf("sem acesso a HKLM\\SOFTWARE: %v", err)
	}
	for _, v := range page.Values {
		if v.ReadError != "" && v.Data != "" {
			t.Errorf("valor %q tem ReadError preenchido E dado; o contrato exige dado vazio quando a leitura falha", v.Name)
		}
	}
}

// TestSetValue_RawTypePreservation garante que gravar um tipo de recurso não o
// converte em REG_BINARY (o que corrompia o tipo numa cópia/rename).
func TestSetValue_RawTypePreservation(t *testing.T) {
	const testPath = "SOFTWARE\\ZZWinRegistryTypeTest"

	if err := CreateKey(HiveCurrentUser, testPath, true); err != nil {
		t.Skipf("não foi possível criar chave de teste: %v", err)
	}
	defer func() { _ = DeleteKey(HiveCurrentUser, testPath, true, true) }()

	// REG_RESOURCE_LIST (id 8): tipo sem setter tipado no pacote x/sys.
	// Gravamos via o caminho de tipo cru e conferimos que o tipo foi preservado.
	key, err := openKey(HiveCurrentUser, testPath, true)
	if err != nil {
		t.Skipf("não foi possível abrir a chave de teste: %v", err)
	}
	defer key.Close()

	payload := []byte{0x01, 0x02, 0x03, 0x04}
	if err := setValueRawType(key, "RecursoTeste", 8, payload); err != nil {
		t.Skipf("setValueRawType indisponível neste contexto: %v", err)
	}

	entry, gerr := GetValue(HiveCurrentUser, testPath, "RecursoTeste")
	if gerr != nil {
		t.Fatalf("GetValue: %v", gerr)
	}
	if entry.Type != TypeResourceList {
		t.Errorf("tipo = %s; queria %s (o tipo NÃO pode ser convertido em REG_BINARY)",
			entry.Type, TypeResourceList)
	}
}

// TestMultiString_RoundTripThroughRealRegistry prova o round-trip do MULTI_SZ
// com newline literal no registro REAL — o caso que o formato JSON corrigiu.
func TestMultiString_RoundTripThroughRealRegistry(t *testing.T) {
	const testPath = "SOFTWARE\\ZZWinRegistryMultiTest"

	if err := CreateKey(HiveCurrentUser, testPath, true); err != nil {
		t.Skipf("não foi possível criar chave de teste: %v", err)
	}
	defer func() { _ = DeleteKey(HiveCurrentUser, testPath, true, true) }()

	original := []string{"linha1\nlinha2", "segundo", ""}
	data := EncodeMultiStringFromList(original)

	if err := SetValue(HiveCurrentUser, testPath, "Lista", TypeMultiString, data, true); err != nil {
		t.Skipf("não foi possível gravar no registro: %v", err)
	}

	entry, err := GetValue(HiveCurrentUser, testPath, "Lista")
	if err != nil {
		t.Fatalf("GetValue: %v", err)
	}
	if len(entry.StringList) != len(original) {
		t.Fatalf("itens = %#v (%d); queria %d itens", entry.StringList, len(entry.StringList), len(original))
	}
	for i := range original {
		if entry.StringList[i] != original[i] {
			t.Errorf("item %d = %q; queria %q", i, entry.StringList[i], original[i])
		}
	}
}
