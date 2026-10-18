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
	regPathWUPolicyAU = `SOFTWAREPoliciesMicrosoftWindowsWindowsUpdateAU`
	regPathWUPolicy   = `SOFTWAREPoliciesMicrosoftWindowsWindowsUpdate`

	policyScopeAU = "AU"
	policyScopeWU = "WU"
)

// readPolicy le as chaves de politica de Windows Update (Policies). Valores
// ausentes ficam nil (nao gerenciados pelo agente).
func readPolicy() (PolicySettings, error) {
	settings := PolicySettings{}

	au, err := registry.OpenKey(registry.LOCAL_MACHINE, regPathWUPolicyAU, registry.QUERY_VALUE)
	if err == nil {
		defer au.Close()
		settings.NoAutoUpdate = readPolicyDWORD(au, "NoAutoUpdate")
		settings.AUOptions = readPolicyDWORD(au, "AUOptions")
	}
	wu, err := registry.OpenKey(registry.LOCAL_MACHINE, regPathWUPolicy, registry.QUERY_VALUE)
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

	auKey, _, err := registry.CreateKey(registry.LOCAL_MACHINE, regPathWUPolicyAU, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, fmt.Errorf("abrir chave de politica AU: %w", err)
	}
	defer auKey.Close()

	wuKey, _, err := registry.CreateKey(registry.LOCAL_MACHINE, regPathWUPolicy, registry.QUERY_VALUE|registry.SET_VALUE)
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
func restorePolicy(backup PolicyBackup) error {
	auKey, _, err := registry.CreateKey(registry.LOCAL_MACHINE, regPathWUPolicyAU, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer auKey.Close()
	wuKey, _, err := registry.CreateKey(registry.LOCAL_MACHINE, regPathWUPolicy, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer wuKey.Close()

	var firstErr error
	for name, value := range backup.Values {
		scope, valueName := splitPolicyName(name)
		var key registry.Key
		switch scope {
		case policyScopeAU:
			key = auKey
		case policyScopeWU:
			key = wuKey
		default:
			continue
		}
		if err := writePolicyValue(key, valueName, value); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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
