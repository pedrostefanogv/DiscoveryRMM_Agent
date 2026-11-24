//go:build windows

package winregistry

import (
	"fmt"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// ─────────────────────────────────────────────────────────────────────────────
// ABERTURA DE CHAVES (camada fina sobre golang.org/x/sys/windows/registry)
//
// Responsabilidades:
//   - traduzir Hive canonico -> registry.Key;
//   - aplicar a visao 64-bit (WOW64_64KEY);
//   - traduzir ERROR_FILE_NOT_FOUND / ERROR_ACCESS_DENIED para os sentinelas
//     do pacote, para que a UI distinga "nao existe" de "sem privilegio".
//
// NAO aplica politica: quem decide o que e permitido e guard.go, chamado antes
// por ops_windows.go. Manter a separacao e o que torna o guard testavel sem
// tocar no registro.
// ─────────────────────────────────────────────────────────────────────────────

// hiveRoot devolve a raiz do registro para um hive canonico.
//
// HKCU e resolvido para a conta em que o agente roda (SYSTEM, no servico).
// Isso significa que "HKCU" NAO e a chave do usuario interativo — e a do
// SYSTEM. A UI precisa dizer isso explicitamente ao operador; ler a HKCU do
// usuario logado exigiria carregar o hive dele (load/unload), que e operacao
// de risco e fica para uma versao futura. Ver hiveViewNote.
func hiveRoot(hive Hive) (registry.Key, bool) {
	switch hive {
	case HiveLocalMachine:
		return registry.LOCAL_MACHINE, true
	case HiveCurrentUser:
		return registry.CURRENT_USER, true
	case HiveClassesRoot:
		return registry.CLASSES_ROOT, true
	case HiveUsers:
		return registry.USERS, true
	case HiveCurrentConfig:
		return registry.CURRENT_CONFIG, true
	default:
		return 0, false
	}
}

// openMode devolve os acessos pedidos ao abrir uma chave.
//
// WOW64_64KEY e explicito: sem ele, um agente 32-bit veria a visao WOW64
// (redirecionada) e o operador acharia que a chave "nao existe". Mesma escolha
// ja usada em app/agent_commands_decommission_windows.go:25.
func openMode(forMutation bool) uint32 {
	mode := uint32(registry.WOW64_64KEY | registry.QUERY_VALUE | registry.ENUMERATE_SUB_KEYS)
	if forMutation {
		mode |= registry.SET_VALUE | registry.CREATE_SUB_KEY
	}
	return mode
}

// resolveTarget traduz (hive, normalizedPath) para a chave raiz real do SO e
// o caminho fisico correspondente.
//
// Quando hive == HiveCurrentUser e ha um usuario interativo conectado com hive
// montado em HKEY_USERS\<SID>, redireciona automaticamente para
// (registry.USERS, "<SID>\\<normalizedPath>"), evitando que HKCU exiba a conta
// SYSTEM do servico. Caso nao haja usuario logado, faz fallback para
// registry.CURRENT_USER.
func resolveTarget(hive Hive, normalizedPath string) (registry.Key, string, *UserContext, error) {
	if hive == HiveCurrentUser {
		if user := activeUserResolver(); user != nil && user.SID != "" {
			osPath := user.SID
			if normalizedPath != "" {
				osPath = user.SID + "\\" + normalizedPath
			}
			return registry.USERS, osPath, user, nil
		}
	}
	root, ok := hiveRoot(hive)
	if !ok {
		return 0, "", nil, ErrInvalidHive
	}
	return root, normalizedPath, nil, nil
}

// openKeyWithUser abre uma chave existente e devolve tambem o UserContext
// efetivamente resolvido (nao-nil quando HKCU foi mapeado para um usuario).
func openKeyWithUser(hive Hive, keyPath string, forMutation bool) (registry.Key, *UserContext, error) {
	root, osPath, user, err := resolveTarget(hive, keyPath)
	if err != nil {
		return 0, nil, err
	}
	key, err := registry.OpenKey(root, osPath, openMode(forMutation))
	if err != nil {
		return 0, nil, mapRegistryError(err, keyPath)
	}
	return key, user, nil
}

// openKey abre uma chave existente, mapeando os erros do SO.
func openKey(hive Hive, keyPath string, forMutation bool) (registry.Key, error) {
	key, _, err := openKeyWithUser(hive, keyPath, forMutation)
	return key, err
}

// createKey opens or creates a key (for CreateKey / SetValue on a new path).
func createKeyHandle(hive Hive, keyPath string) (registry.Key, error) {
	root, osPath, _, err := resolveTarget(hive, keyPath)
	if err != nil {
		return 0, err
	}
	mode := openMode(true) | registry.WRITE
	key, _, err := registry.CreateKey(root, osPath, mode)
	if err != nil {
		return 0, mapRegistryError(err, keyPath)
	}
	return key, nil
}

// mapRegistryError traduz o erro do SO para o sentinela do pacote.
//
// A distincao "nao existe" vs "acesso negado" e o ponto todo: sem ela o
// operador ve "erro ao abrir a chave" nos dois casos e nao sabe se errou o
// caminho ou se o agente nao tem privilegio — a diferenca entre corrigir o
// caminho e pedir elevacao.
func mapRegistryError(err error, keyPath string) error {
	if err == nil {
		return nil
	}
	// O pacote x/sys expoe apenas ErrNotExist e ErrShortBuffer como sentinelas;
	// ERROR_ACCESS_DENIED chega como syscall.Errno cru.
	if err == registry.ErrNotExist {
		return ErrKeyNotFound
	}
	if errno, ok := err.(syscall.Errno); ok {
		switch errno {
		case syscall.ERROR_FILE_NOT_FOUND, syscall.ERROR_PATH_NOT_FOUND:
			return ErrKeyNotFound
		case syscall.ERROR_ACCESS_DENIED:
			// Sentinela PROPRIA: nao e uma decisao do guard, e a ACL do SO.
			return fmt.Errorf("%w: %q (o contexto em que o agente roda nao tem privilegio nesta chave)", ErrAccessDenied, keyPath)
		}
	}
	return err
}

// hiveViewNote documenta a limitacao de visao do hive, exposta ao operador.
//
// Nao e enfeite: sem esta nota, "HKCU aparece com as chaves do SYSTEM" e
// reportado como bug. Melhor avisar na propria resposta do protocolo.
const hiveViewNote = "HKCU e a chave da conta em que o agente roda (SYSTEM no servico), " +
	"nao a do usuario interativo; visao 64-bit (WOW64_64KEY)"

// normalizeAndCheck aplica guard + normalizacao numa chamada so, devolvendo o
// caminho ja normalizado. Centraliza a ORDEM (normaliza, depois avalia) para
// que nenhuma operacao possa esquecer o guard.
func normalizeAndCheck(hive Hive, keyPath string, op Op) (string, GuardDecision, error) {
	normalized, err := NormalizePath(keyPath)
	if err != nil {
		return "", GuardDecision{}, err
	}
	decision := Evaluate(hive, normalized, op)
	if !decision.Allowed {
		return normalized, decision, fmt.Errorf("%w: %s", ErrProtectedKey, decision.Reason)
	}
	return normalized, decision, nil
}

// guardSuffixForLog devolve um rotulo curto para auditoria.
func guardSuffixForLog(decision GuardDecision) string {
	if decision.Allowed {
		return "allowed"
	}
	if decision.Rule == "" {
		return "denied"
	}
	return "denied:" + decision.Rule
}
