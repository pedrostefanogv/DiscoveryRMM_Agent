//go:build windows

package native

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// StartupItemTarget identifica um item de inicialização para habilitar/
// desabilitar. Os campos correspondem ao item coletado por
// collectStartupItemsNative (Type/Name/Source/Username).
type StartupItemTarget struct {
	Type   string // registry | folder | service
	Name   string // valor do registro, arquivo .lnk ou nome do serviço
	Source string // origem coletada (ex.: "HKLM Run", "Pasta Startup (Usuário)")
	// Hive identifica a conta do item: "HKLM", "HKCU" ou "HKU:<SID>".
	// Necessário para itens de outros usuários (lidos via HKEY_USERS quando
	// o serviço roda como SYSTEM).
	Hive string
}

// SetStartupItemEnabled habilita ou desabilita um item de inicialização.
//
// Para itens de registro/pasta a operação replica o comportamento do
// Gerenciador de Tarefas: grava um valor binário na chave StartupApproved
// correspondente (02 = habilitado, 03 = desabilitado) — reversível e sem
// tocar no valor original do item.
//
// Para serviços altera o tipo de inicialização no registro
// (Start: 4 = Disabled, 2 = Automatic, preservando DelayedAutostart).
func SetStartupItemEnabled(enable bool, target StartupItemTarget) error {
	itemType := strings.ToLower(strings.TrimSpace(target.Type))
	switch itemType {
	case "service":
		return setServiceStartMode(enable, target.Name)
	case "registry":
		ref, err := approvedKeyForTarget(target, []string{"Run"})
		if err != nil {
			return err
		}
		return setStartupApprovedState(enable, ref, target.Name)
	case "folder":
		ref, err := approvedKeyForTarget(target, []string{"StartupFolder"})
		if err != nil {
			return err
		}
		return setStartupApprovedState(enable, ref, target.Name)
	default:
		return fmt.Errorf("tipo de item de inicialização não suportado: %q", itemType)
	}
}

type approvedKeyRef struct {
	root     registry.Key
	hivePath string // prefixo dentro do root (SID quando root é HKEY_USERS)
	subkeys  []string
}

// approvedKeyForSource resolve a chave StartupApproved correta para a origem
// do item (subchaves candidatas, consultadas em ordem). O hive vem da origem,
// mas é sobrescrito pelo Hive explícito do item quando presente.
func approvedKeyForSource(source string) approvedKeyRef {
	switch source {
	case srcHKLMRun:
		return approvedKeyRef{root: registry.LOCAL_MACHINE, subkeys: []string{"Run"}}
	case srcHKLMRunOnce:
		return approvedKeyRef{root: registry.LOCAL_MACHINE, subkeys: []string{"RunOnce", "Run32"}}
	case srcHKLMRun32:
		return approvedKeyRef{root: registry.LOCAL_MACHINE, subkeys: []string{"Run32", "Run"}}
	case srcHKLMRunOnce32:
		return approvedKeyRef{root: registry.LOCAL_MACHINE, subkeys: []string{"Run32", "RunOnce"}}
	case srcHKCURun:
		return approvedKeyRef{root: registry.CURRENT_USER, subkeys: []string{"Run"}}
	case srcHKCURunOnce:
		return approvedKeyRef{root: registry.CURRENT_USER, subkeys: []string{"RunOnce"}}
	case srcHKCURun32:
		return approvedKeyRef{root: registry.CURRENT_USER, subkeys: []string{"Run32", "Run"}}
	case srcHKCURunOnce32:
		return approvedKeyRef{root: registry.CURRENT_USER, subkeys: []string{"Run32", "RunOnce"}}
	default:
		return approvedKeyRef{root: registry.CURRENT_USER, subkeys: []string{"Run"}}
	}
}

// approvedKeyForTarget combina a origem (subchaves) com o hive do item.
func approvedKeyForTarget(target StartupItemTarget, fallbackSubkeys []string) (approvedKeyRef, error) {
	ref := approvedKeyForSource(target.Source)
	if len(ref.subkeys) == 0 {
		ref.subkeys = fallbackSubkeys
	}
	return applyHive(ref, target.Hive)
}

// applyHive sobrescreve root/hivePath quando o item traz o hive explícito.
func applyHive(ref approvedKeyRef, hive string) (approvedKeyRef, error) {
	h := strings.TrimSpace(hive)
	switch {
	case h == "":
		return ref, nil
	case strings.EqualFold(h, hiveHKCU):
		ref.root = registry.CURRENT_USER
		ref.hivePath = ""
	case strings.EqualFold(h, hiveHKLM):
		ref.root = registry.LOCAL_MACHINE
		ref.hivePath = ""
	case strings.HasPrefix(strings.ToUpper(h), strings.ToUpper(hiveHKUPrefix)):
		sid := strings.TrimSpace(h[len(hiveHKUPrefix):])
		if sid == "" || strings.ContainsAny(sid, `\/`) {
			return ref, fmt.Errorf("hive HKU inválido: %q", hive)
		}
		ref.root = registry.Key(windows.HKEY_USERS)
		ref.hivePath = sid
	default:
		return ref, fmt.Errorf("hive de item de inicialização não suportado: %q", hive)
	}
	return ref, nil
}

// startupApprovedPrefix retorna o prefixo da chave StartupApproved, já
// considerando o hivePath (SID) quando o item pertence a outro usuário.
func startupApprovedPrefix(ref approvedKeyRef) string {
	return joinRegPath(ref.hivePath, approvedPrefixFor(ref.root))
}

// firstExistingSubkey retorna a primeira subchave existente (ex.: Run32 antes
// de Run para itens Wow64). Se nenhuma existir, retorna a primeira candidata
// (o OpenKey acima reporta o erro adequado).
func firstExistingSubkey(ref approvedKeyRef, subkeys []string) string {
	for _, sub := range subkeys {
		if _, err := registry.OpenKey(ref.root, joinRegPath(startupApprovedPrefix(ref), sub), windows.KEY_READ); err == nil {
			return sub
		}
	}
	if len(subkeys) > 0 {
		return subkeys[0]
	}
	return "Run"
}

// setStartupApprovedState grava o valor binário de StartupApproved do item.
// Escrever em StartupApproved (HKLM) exige privilégio administrativo — o
// serviço do agent roda como SYSTEM, mas o processo de UI pode falhar com
// Access Denied; o erro é reportado ao servidor para exibição ao usuário.
func setStartupApprovedState(enable bool, ref approvedKeyRef, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("nome do item de inicialização é obrigatório")
	}
	prefix := startupApprovedPrefix(ref)
	path := joinRegPath(prefix, firstExistingSubkey(ref, ref.subkeys))
	key, err := registry.OpenKey(ref.root, path, windows.KEY_SET_VALUE|windows.KEY_READ)
	if err != nil {
		// Chave StartupApproved pode não existir: criar sob a primeira candidata.
		key, _, err = registry.CreateKey(ref.root, joinRegPath(prefix, ref.subkeys[0]), windows.KEY_SET_VALUE)
		if err != nil {
			return fmt.Errorf("falha ao abrir/criar StartupApproved (%s): %w", strings.Join(ref.subkeys, "/"), err)
		}
	}
	defer key.Close()

	// 12 bytes no padrão do Windows: byte 0 = flag (02 habilitado,
	// 03 desabilitado) + FILETIME de 8 bytes do momento da alteração.
	data := make([]byte, 12)
	if enable {
		data[0] = 0x02
	} else {
		data[0] = 0x03
	}
	if err := key.SetBinaryValue(name, data); err != nil {
		return fmt.Errorf("falha ao gravar StartupApproved\\%s\\%s: %w", strings.Join(ref.subkeys, "/"), name, err)
	}
	return nil
}

// setServiceStartMode altera o tipo de inicialização de um serviço.
func setServiceStartMode(enable bool, serviceName string) error {
	name := strings.TrimSpace(serviceName)
	if name == "" {
		return fmt.Errorf("nome do serviço é obrigatório")
	}
	start := 2 // Automatic
	if !enable {
		start = 4 // Disabled
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, servicesKeyPath+"\\"+name, windows.KEY_SET_VALUE|windows.KEY_READ)
	if err != nil {
		return fmt.Errorf("serviço %q não encontrado: %w", name, err)
	}
	defer key.Close()
	if err := key.SetDWordValue("Start", uint32(start)); err != nil {
		return fmt.Errorf("falha ao alterar tipo de inicialização do serviço %s: %w", name, err)
	}
	return nil
}
