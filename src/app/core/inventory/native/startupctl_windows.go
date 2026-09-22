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
		return setStartupApprovedState(enable, approvedKeyForSource(target.Source), target.Name)
	case "folder":
		root := registry.CURRENT_USER
		if strings.EqualFold(strings.TrimSpace(target.Source), srcStartupCommon) {
			root = registry.LOCAL_MACHINE
		}
		return setStartupApprovedState(enable, approvedKeyRef{root: root, subkeys: []string{"StartupFolder"}}, target.Name)
	default:
		return fmt.Errorf("tipo de item de inicialização não suportado: %q", itemType)
	}
}

type approvedKeyRef struct {
	root    registry.Key
	subkeys []string
}

// approvedKeyForSource resolve a chave StartupApproved correta para a origem
// do item (hive + subchaves candidatas, consultadas em ordem).
func approvedKeyForSource(source string) approvedKeyRef {
	switch source {
	case srcHKLMRun:
		return approvedKeyRef{registry.LOCAL_MACHINE, []string{"Run"}}
	case srcHKLMRunOnce:
		return approvedKeyRef{registry.LOCAL_MACHINE, []string{"RunOnce", "Run32"}}
	case srcHKLMRun32:
		return approvedKeyRef{registry.LOCAL_MACHINE, []string{"Run32", "Run"}}
	case srcHKLMRunOnce32:
		return approvedKeyRef{registry.LOCAL_MACHINE, []string{"Run32", "RunOnce"}}
	case srcHKCURun:
		return approvedKeyRef{registry.CURRENT_USER, []string{"Run"}}
	case srcHKCURunOnce:
		return approvedKeyRef{registry.CURRENT_USER, []string{"RunOnce"}}
	default:
		return approvedKeyRef{registry.CURRENT_USER, []string{"Run"}}
	}
}

// startupApprovedPrefix retorna o prefixo da chave StartupApproved no hive.
func startupApprovedPrefix(root registry.Key) string {
	if root == registry.CURRENT_USER {
		return "Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\StartupApproved\\"
	}
	return "SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Explorer\\StartupApproved\\"
}

// firstExistingSubkey retorna a primeira subchave existente (ex.: Run32 antes
// de Run para itens Wow64). Se nenhuma existir, retorna a primeira candidata
// (o OpenKey acima reporta o erro adequado).
func firstExistingSubkey(root registry.Key, subkeys []string) string {
	for _, sub := range subkeys {
		if _, err := registry.OpenKey(root, startupApprovedPrefix(root)+sub, windows.KEY_READ); err == nil {
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
	path := startupApprovedPrefix(ref.root) + firstExistingSubkey(ref.root, ref.subkeys)
	key, err := registry.OpenKey(ref.root, path, windows.KEY_SET_VALUE|windows.KEY_READ)
	if err != nil {
		// Chave StartupApproved pode não existir: criar sob a primeira candidata.
		key, _, err = registry.CreateKey(ref.root, startupApprovedPrefix(ref.root)+ref.subkeys[0], windows.KEY_SET_VALUE)
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
