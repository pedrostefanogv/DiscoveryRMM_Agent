//go:build windows

package windowsupdate

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// Os caminhos das chaves ficam em policy_paths.go (fonte unica). Aqui so
	// existem os nomes de escopo usados no backup/rollback.
	policyScopeAU = "AU"
	policyScopeWU = "WU"
)

// readPolicy le as chaves de politica de Windows Update (Policies). Valores
// ausentes ficam nil (nao gerenciados pelo agente).
func readPolicy() (PolicySettings, error) {
	settings := PolicySettings{}

	au, err := registry.OpenKey(registry.LOCAL_MACHINE, regPathWUPoliciesAU, registry.QUERY_VALUE)
	if err == nil {
		defer au.Close()
		settings.NoAutoUpdate = readPolicyDWORD(au, "NoAutoUpdate")
		settings.AUOptions = readPolicyDWORD(au, "AUOptions")
	}
	wu, err := registry.OpenKey(registry.LOCAL_MACHINE, regPathWUPolicies, registry.QUERY_VALUE)
	if err == nil {
		defer wu.Close()
		settings.DeferQualityUpdates = readPolicyDWORD(wu, "DeferQualityUpdates")
		settings.DeferQualityUpdatesPeriodInDays = readPolicyDWORD(wu, "DeferQualityUpdatesPeriodInDays")
		settings.PauseQualityUpdates = readPolicyDWORD(wu, "PauseQualityUpdates")
		settings.PauseQualityUpdatesStartTime = readPolicyString(wu, "PauseQualityUpdatesStartTime")
	}
	return settings, nil
}

// applyPolicy aplica os valores informados e devolve o backup dos valores
// anteriores. Aborta quando a maquina e gerenciada por WSUS/GPO.
func applyPolicy(settings PolicySettings) (*PolicyBackup, error) {
	if wsusServerFromPolicy() != "" {
		return nil, ErrPolicyManagedByGPO
	}

	backup := &PolicyBackup{
		CapturedAt: time.Now().UTC(),
		Values:     make(map[string]PolicyValue),
	}

	auKey, auExisted, err := registry.CreateKey(registry.LOCAL_MACHINE, regPathWUPoliciesAU, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, fmt.Errorf("abrir chave de politica AU: %w", err)
	}
	defer auKey.Close()

	// WU-16/D04: nunca sobrescrever politica que nao e nossa. Se ja existe um
	// NoAutoUpdate sem a nossa sentinela, quem escreveu foi GPO/administrador.
	existingNoAutoUpdate := readPolicyDWORD(auKey, "NoAutoUpdate")
	discoveryManaged := readPolicyDWORD(auKey, discoveryManagedValue) != nil
	if !PolicyWriteAllowed(existingNoAutoUpdate, discoveryManaged) {
		return nil, ErrPolicyManagedByGPO
	}

	// Marca propriedade ANTES de escrever a politica: se a escrita falhar no
	// meio, o revert ainda sabe que a chave/valor podem ser nossos.
	backup.Values[policyScopeAU+"\\"+discoveryManagedValue] = readPolicyValue(auKey, discoveryManagedValue)
	if err := auKey.SetDWordValue(discoveryManagedValue, 1); err != nil {
		return nil, fmt.Errorf("marcar propriedade da politica AU: %w", err)
	}
	if !auExisted {
		backup.Values[policyScopeAU+"\\"+discoveryCreatedKeyValue] = readPolicyValue(auKey, discoveryCreatedKeyValue)
		if err := auKey.SetDWordValue(discoveryCreatedKeyValue, 1); err != nil {
			return nil, fmt.Errorf("marcar criacao da chave AU: %w", err)
		}
	}

	wuKey, _, err := registry.CreateKey(registry.LOCAL_MACHINE, regPathWUPolicies, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, fmt.Errorf("abrir chave de politica WU: %w", err)
	}
	defer wuKey.Close()

	apply := func(scope string, key registry.Key, name string, value *uint32) error {
		backup.Values[scope+"\\"+name] = readPolicyValue(key, name)
		if value == nil {
			return nil
		}
		return key.SetDWordValue(name, *value)
	}

	if err := apply(policyScopeAU, auKey, "NoAutoUpdate", settings.NoAutoUpdate); err != nil {
		return nil, err
	}
	if err := apply(policyScopeAU, auKey, "AUOptions", settings.AUOptions); err != nil {
		return nil, err
	}
	if err := apply(policyScopeWU, wuKey, "DeferQualityUpdates", settings.DeferQualityUpdates); err != nil {
		return nil, err
	}
	if err := apply(policyScopeWU, wuKey, "DeferQualityUpdatesPeriodInDays", settings.DeferQualityUpdatesPeriodInDays); err != nil {
		return nil, err
	}
	if err := apply(policyScopeWU, wuKey, "PauseQualityUpdates", settings.PauseQualityUpdates); err != nil {
		return nil, err
	}

	// PauseQualityUpdatesStartTime e REG_SZ.
	backup.Values[policyScopeWU+"\\PauseQualityUpdatesStartTime"] = readPolicyValue(wuKey, "PauseQualityUpdatesStartTime")
	if settings.PauseQualityUpdatesStartTime != nil {
		if err := wuKey.SetStringValue("PauseQualityUpdatesStartTime", *settings.PauseQualityUpdatesStartTime); err != nil {
			return nil, err
		}
	}

	return backup, nil
}

// restorePolicy devolve os valores ao estado capturado no backup.
//
// Não CRIA chaves por si só: só cria a chave do escopo quando existe valor a
// restaurar nela (antes, um revert criava Policies\...\WindowsUpdate vazias na
// máquina, mesmo sem nada para restaurar).
func restorePolicy(backup PolicyBackup) error {
	keys := make(map[string]registry.Key, 2)
	closeKeys := func() {
		for _, key := range keys {
			_ = key.Close()
		}
	}

	openOrCreate := func(path string, needed bool) (registry.Key, bool) {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.SET_VALUE)
		if err == nil {
			return key, true
		}
		if !needed {
			return 0, false
		}
		created, _, cerr := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.SET_VALUE)
		if cerr != nil {
			return 0, false
		}
		return created, true
	}

	if auKey, ok := openOrCreate(regPathWUPoliciesAU, backupHasScope(backup, policyScopeAU)); ok {
		keys[policyScopeAU] = auKey
	}
	if wuKey, ok := openOrCreate(regPathWUPolicies, backupHasScope(backup, policyScopeWU)); ok {
		keys[policyScopeWU] = wuKey
	}

	var firstErr error
	for name, value := range backup.Values {
		scope, valueName := splitPolicyName(name)
		key, ok := keys[scope]
		if !ok {
			continue
		}
		if err := writePolicyValue(key, valueName, value); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// WU-16/D04: limpa as sentinelas de propriedade. Se a chave AU foi criada
	// por nós e ficou vazia, remove a chave — sem tocar em valores alheios.
	auKey, hasAU := keys[policyScopeAU]
	if !hasAU {
		closeKeys()
		return firstErr
	}

	managed := readPolicyDWORD(auKey, discoveryManagedValue) != nil
	createdByUs := readPolicyDWORD(auKey, discoveryCreatedKeyValue) != nil
	if managed {
		_ = auKey.DeleteValue(discoveryManagedValue)
	}
	if createdByUs {
		_ = auKey.DeleteValue(discoveryCreatedKeyValue)
	}

	removeKey := false
	if managed && createdByUs {
		if valueNames, vErr := auKey.ReadValueNames(-1); vErr == nil {
			if subKeys, sErr := auKey.ReadSubKeyNames(-1); sErr == nil {
				removeKey = len(valueNames) == 0 && len(subKeys) == 0
			}
		}
	}

	// Fecha os handles ANTES de remover a chave (o Windows não remove chave
	// com handle aberto).
	closeKeys()
	if removeKey {
		_ = registry.DeleteKey(registry.LOCAL_MACHINE, regPathWUPoliciesAU)
	}
	return firstErr
}

// backupHasScope informa se o backup tem algum valor do escopo informado.
func backupHasScope(backup PolicyBackup, scope string) bool {
	for name := range backup.Values {
		if strings.HasPrefix(name, scope+`\\`) {
			return true
		}
	}
	return false
}

func splitPolicyName(name string) (scope, valueName string) {
	parts := strings.SplitN(name, "\\", 2)
	if len(parts) != 2 {
		return "", name
	}
	return parts[0], parts[1]
}

func readPolicyDWORD(key registry.Key, name string) *uint32 {
	value, _, err := key.GetIntegerValue(name)
	if err != nil {
		return nil
	}
	v := uint32(value)
	return &v
}

func readPolicyString(key registry.Key, name string) *string {
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return nil
	}
	v := value
	return &v
}

// readPolicyValue captura qualquer tipo de valor (para rollback fiel).
func readPolicyValue(key registry.Key, name string) PolicyValue {
	buf := make([]byte, 4096)
	n, valType, err := key.GetValue(name, buf)
	if err != nil {
		return PolicyValue{Exists: false}
	}
	buf = buf[:n]
	value := PolicyValue{Exists: true}
	switch valType {
	case registry.SZ, registry.EXPAND_SZ:
		value.Type = "sz"
		value.String = utf16BytesToString(buf)
	case registry.DWORD:
		value.Type = "dword"
		if len(buf) >= 4 {
			value.Number = uint64(binary.LittleEndian.Uint32(buf))
		}
	case registry.QWORD:
		value.Type = "qword"
		if len(buf) >= 8 {
			value.Number = binary.LittleEndian.Uint64(buf)
		}
	case registry.BINARY:
		value.Type = "binary"
		value.Binary = append([]byte(nil), buf...)
	case registry.MULTI_SZ:
		value.Type = "multi_sz"
		value.Strings = splitMultiSz(buf)
	default:
		value.Type = "binary"
		value.Binary = append([]byte(nil), buf...)
	}
	return value
}

func writePolicyValue(key registry.Key, name string, value PolicyValue) error {
	if !value.Exists {
		err := key.DeleteValue(name)
		if err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	switch value.Type {
	case "sz":
		return key.SetStringValue(name, value.String)
	case "dword":
		return key.SetDWordValue(name, uint32(value.Number))
	case "qword":
		return key.SetQWordValue(name, value.Number)
	case "binary":
		return key.SetBinaryValue(name, value.Binary)
	case "multi_sz":
		return key.SetStringsValue(name, value.Strings)
	default:
		return nil
	}
}

// splitMultiSz separa um buffer MULTI_SZ em strings.
func splitMultiSz(buf []byte) []string {
	values := make([]string, 0, 4)
	current := make([]uint16, 0, len(buf)/2)
	for i := 0; i+1 < len(buf); i += 2 {
		unit := uint16(buf[i]) | uint16(buf[i+1])<<8
		if unit == 0 {
			if len(current) == 0 {
				break
			}
			values = append(values, windows.UTF16ToString(current))
			current = current[:0]
			continue
		}
		current = append(current, unit)
	}
	if len(current) > 0 {
		values = append(values, windows.UTF16ToString(current))
	}
	return values
}
