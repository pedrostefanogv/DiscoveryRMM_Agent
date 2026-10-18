//go:build windows

package windowsupdate

import (
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	regPathWURebootRequired = `SOFTWAREMicrosoftWindowsCurrentVersionWindowsUpdateAuto UpdateRebootRequired`
	regPathCBSRebootPending = `SOFTWAREMicrosoftWindowsCurrentVersionComponent Based ServicingRebootPending`
	regPathCBSPackages      = `SOFTWAREMicrosoftWindowsCurrentVersionComponent Based ServicingPackages`
	regPathSessionManager   = `SYSTEMCurrentControlSetControlSession Manager`
	regPathTcpipParameters  = `SYSTEMCurrentControlSetServicesTcpipParameters`
	regPathComputerName     = `SYSTEMCurrentControlSetControlComputerNameComputerName`
	regPathActiveComputer   = `SYSTEMCurrentControlSetControlComputerNameActiveComputerName`
	regPathWUAService       = `SYSTEMCurrentControlSetServiceswuauserv`
)

// probePendingReboot le os indicadores de reinicio pendente no registro.
// A sonda e barata (somente leitura) e roda a cada consolidacao de inventario
// ou a cada 10 min no loop do agente.
func probePendingReboot() RebootInfo {
	info := RebootInfo{CheckedAt: time.Now().UTC()}
	reasons := make(map[string]struct{})

	if registryKeyExists(regPathWURebootRequired) {
		reasons[RebootReasonWindowsUpdate] = struct{}{}
	}
	if registryKeyExists(regPathCBSRebootPending) {
		reasons[RebootReasonCBS] = struct{}{}
	}
	if pendingFileRenameOperations() {
		reasons[RebootReasonFileRename] = struct{}{}
	}
	if pendingComputerRename() {
		reasons[RebootReasonComputerRename] = struct{}{}
	}

	// Pacotes CBS só precisam ser enumerados se ainda não há motivo conhecido:
	// a varredura percorre milhares de subchaves e a sonda roda a cada 10 min.
	if len(reasons) == 0 && pendingServicingOperations() {
		reasons[RebootReasonServicing] = struct{}{}
	}

	for reason := range reasons {
		info.Reasons = append(info.Reasons, reason)
	}
	sortReasons(info.Reasons)
	info.Pending = len(info.Reasons) > 0
	return info
}

// sortReasons mantem a ordem estavel no JSON (map iteration e aleatoria).
func sortReasons(reasons []string) {
	order := map[string]int{
		RebootReasonWindowsUpdate:  0,
		RebootReasonCBS:            1,
		RebootReasonServicing:      2,
		RebootReasonFileRename:     3,
		RebootReasonComputerRename: 4,
	}
	for i := 1; i < len(reasons); i++ {
		for j := i; j > 0; j-- {
			if order[reasons[j-1]] <= order[reasons[j]] {
				break
			}
			reasons[j-1], reasons[j] = reasons[j], reasons[j-1]
		}
	}
}

func registryKeyExists(path string) bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return false
	}
	key.Close()
	return true
}

// pendingFileRenameOperations retorna true quando ha arquivos aguardando
// renomeacao no boot (atualizacoes de drivers/instaladores).
func pendingFileRenameOperations() bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, regPathSessionManager, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	values, _, err := key.GetStringsValue("PendingFileRenameOperations")
	if err != nil {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// pendingComputerRename compara o nome ativo com o nome pendente.
func pendingComputerRename() bool {
	active := registryStringValue(regPathActiveComputer, "ComputerName")
	pending := registryStringValue(regPathComputerName, "ComputerName")
	if active == "" || pending == "" {
		return false
	}
	return !strings.EqualFold(active, pending)
}

// pendingServicingOperations detecta pacotes CBS com XML pendente. Limitado a
// 5000 subchaves para nao custar caro em maquinas com muitos pacotes.
func pendingServicingOperations() bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, regPathCBSPackages, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return false
	}
	defer key.Close()
	names, err := key.ReadSubKeyNames(5000)
	if err != nil && len(names) == 0 {
		return false
	}
	for _, name := range names {
		pkg, err := registry.OpenKey(key, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		_, _, valueErr := pkg.GetValue("PendingXmlIdentifier", nil)
		pkg.Close()
		if valueErr == nil {
			return true
		}
	}
	return false
}

func registryStringValue(path, name string) string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// windowsUpdateServiceState reporta o estado do servico Windows Update.
// "disabled" e fatal para o modulo; "manual"/"stopped" nao sao (o WUA inicia
// o servico sob demanda).
func windowsUpdateServiceState() string {
	start := registryDWORDValue(regPathWUAService, "Start")
	if start == 4 {
		return "disabled"
	}

	if m, err := mgr.Connect(); err == nil {
		defer m.Disconnect()
		if s, err := m.OpenService("wuauserv"); err == nil {
			defer s.Close()
			if status, err := s.Query(); err == nil {
				switch status.State {
				case svc.Running:
					return "running"
				case svc.Stopped:
					// Start=3 (manual) e comum e o WUA inicia sob demanda.
					return "stopped"
				case svc.StartPending:
					return "start_pending"
				case svc.StopPending:
					return "stop_pending"
				}
			}
		}
	}

	switch start {
	case 2:
		return "automatic"
	case 3:
		return "manual"
	case 0, 1:
		return "boot"
	}
	return "unknown"
}

func registryDWORDValue(path, name string) uint64 {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer key.Close()
	value, _, err := key.GetIntegerValue(name)
	if err != nil {
		return 0
	}
	return value
}

// wsusServerFromPolicy devolve o WUServer configurado (GPO/registro), que
// indica que a maquina e gerenciada por politica corporativa.
func wsusServerFromPolicy() string {
	for _, path := range []string{
		`SOFTWAREPoliciesMicrosoftWindowsWindowsUpdate`,
		`SOFTWAREMicrosoftWindowsCurrentVersionWindowsUpdateAuto Update`,
	} {
		if server := registryStringValue(path, "WUServer"); server != "" {
			return server
		}
		if server := registryStringValue(path, "UpdateServiceUrlAlternate"); server != "" {
			return server
		}
	}
	return ""
}

// utf16BytesToString converte um buffer UTF-16LE (sem BOM) em string.
func utf16BytesToString(buf []byte) string {
	if len(buf) < 2 {
		return ""
	}
	u16 := make([]uint16, 0, len(buf)/2)
	for i := 0; i+1 < len(buf); i += 2 {
		value := uint16(buf[i]) | uint16(buf[i+1])<<8
		if value == 0 {
			break
		}
		u16 = append(u16, value)
	}
	return windows.UTF16ToString(u16)
}
