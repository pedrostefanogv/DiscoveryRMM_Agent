//go:build windows

package windowsupdate

import (
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Caminhos de registro usados pela sonda de reinicio/estado. Sao escritos como
// raw strings do Go para evitar escapes: o separador e apenas "\".
const (
	regPathWURebootRequired = `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`
	regPathCBSRebootPending = `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`
	regPathCBSPackages      = `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\Packages`
	regPathCBSVolatile      = `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\Volatile`
	regPathSessionManager   = `SYSTEM\CurrentControlSet\Control\Session Manager`
	regPathTcpipParameters  = `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`
	regPathComputerName     = `SYSTEM\CurrentControlSet\Control\ComputerName\ComputerName`
	regPathActiveComputer   = `SYSTEM\CurrentControlSet\Control\ComputerName\ActiveComputerName`
	regPathWUAService       = `SYSTEM\CurrentControlSet\Services\wuauserv`
	regPathSetup            = `SYSTEM\Setup`
	regPathSetupState       = `SYSTEM\Setup\Status\SysprepStatus`
	regPathUninstall        = `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\Packages`
	regPathWindowsUpdateLog = `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\Results\Install`
	regPathWUPolicies       = `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`
	regPathWUAutoUpdate     = `SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update`
)

// ImageState values ("SYSTEM\Setup" -> ImageState) que indicam trabalho de
// servicing pendente de reboot.
const (
	imageStateNone              = 0
	imageStateImageBeingOffline = 1
	imageStateImageBeingApplied = 2
	imageStateImageBootable      = 3
	imageStateImageRetryingBoot  = 4
	imageStateImagePendingRemoval = 5
)

// probeCacheTTL evita reler o registro varias vezes no mesmo ciclo (a sonda
// roda a cada poucos minutos e o scan tambem consulta o estado).
const probeCacheTTL = 20 * time.Second

type rebootProbeCacheEntry struct {
	at   time.Time
	info RebootInfo
}

type pendingRenameCacheEntry struct {
	at   time.Time
	info PendingFileRenameInfo
}

var (
	rebootProbeMu    sync.Mutex
	rebootProbeCache rebootProbeCacheEntry

	pendingRenameMu    sync.Mutex
	pendingRenameCache pendingRenameCacheEntry
)

// probePendingReboot le os indicadores de reinicio pendente no registro.
// A sonda e barata (somente leitura) e roda a cada consolidacao de inventario
// ou a cada ciclo da sonda de estado. O resultado e cacheado por poucos
// segundos para nao repetir a leitura quando o scan pede o mesmo dado.
func probePendingReboot() RebootInfo {
	rebootProbeMu.Lock()
	defer rebootProbeMu.Unlock()
	if !rebootProbeCache.at.IsZero() && time.Since(rebootProbeCache.at) < probeCacheTTL {
		cached := rebootProbeCache.info
		cached.Reasons = append([]string(nil), cached.Reasons...)
		cached.CheckedAt = time.Now().UTC()
		return cached
	}
	info := readPendingReboot()
	rebootProbeCache = rebootProbeCacheEntry{at: time.Now(), info: info}
	return info
}

// invalidateRebootProbeCache descarta o cache (usado depois de instalar).
func invalidateRebootProbeCache() {
	rebootProbeMu.Lock()
	rebootProbeCache = rebootProbeCacheEntry{}
	rebootProbeMu.Unlock()
	pendingRenameMu.Lock()
	pendingRenameCache = pendingRenameCacheEntry{}
	pendingRenameMu.Unlock()
}

// readPendingReboot executa a leitura real dos indicadores.
func readPendingReboot() RebootInfo {
	info := RebootInfo{CheckedAt: time.Now().UTC()}
	reasons := make(map[string]struct{})

	if registryKeyExists(regPathWURebootRequired) {
		reasons[RebootReasonWindowsUpdate] = struct{}{}
	}
	if registryKeyExists(regPathCBSRebootPending) {
		reasons[RebootReasonCBS] = struct{}{}
	}
	if pendingFileRenameOperations().Pending {
		reasons[RebootReasonFileRename] = struct{}{}
	}
	if pendingComputerRename() {
		reasons[RebootReasonComputerRename] = struct{}{}
	}
	if pendingServicingImageState() {
		reasons[RebootReasonServicing] = struct{}{}
	}
	if windowsUpdateRebootRequiredAfterInstall() {
		reasons[RebootReasonWindowsUpdate] = struct{}{}
	}

	// Pacotes CBS so precisam ser enumerados se ainda nao ha motivo conhecido:
	// a varredura percorre milhares de subchaves.
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

// pendingFileRenameOperations le PendingFileRenameOperations e delega a
// decisao para EvaluatePendingFileRename (logica pura e testada).
//
// BUG corrigido: a leitura anterior considerava "vazio" como ausencia de
// operacao e nao entendia entradas "\??\C:\..." — a maquina 019faa6f tinha
// 5 entradas de OneDrive e a sonda reportava pending=false.
func pendingFileRenameOperations() PendingFileRenameInfo {
	pendingRenameMu.Lock()
	if !pendingRenameCache.at.IsZero() && time.Since(pendingRenameCache.at) < probeCacheTTL {
		cached := pendingRenameCache.info
		pendingRenameMu.Unlock()
		return cached
	}
	pendingRenameMu.Unlock()

	info := readPendingFileRenameOperations()

	pendingRenameMu.Lock()
	pendingRenameCache = pendingRenameCacheEntry{at: time.Now(), info: info}
	pendingRenameMu.Unlock()
	return info
}

func readPendingFileRenameOperations() PendingFileRenameInfo {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, regPathSessionManager, registry.QUERY_VALUE)
	if err != nil {
		return PendingFileRenameInfo{}
	}
	defer key.Close()
	values, _, err := key.GetStringsValue("PendingFileRenameOperations")
	if err != nil {
		return PendingFileRenameInfo{}
	}
	return EvaluatePendingFileRename(values)
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

// pendingServicingImageState detecta trabalho de servicing/feature update em
// andamento que exige reboot (SYSTEM\Setup -> ImageState). Barato: dois
// valores no registro.
func pendingServicingImageState() bool {
	state := registryDWORDValue(regPathSetup, "ImageState")
	switch state {
	case imageStateImageBeingOffline, imageStateImageBeingApplied, imageStateImageBootable, imageStateImageRetryingBoot, imageStateImagePendingRemoval:
		return true
	}
	// Sysprep / SetupComplete: "GeneralizationState" != 7 indica sysprep
	// inacabado e "SetupPhase" em andamento.
	if value := registryDWORDValue(regPathSetupState, "GeneralizationState"); value > 0 && value != 7 {
		return true
	}
	if value := registryDWORDValue(regPathSetup, "SystemSetupInProgress"); value == 1 {
		return true
	}
	return false
}

// pendingServicingOperations detecta pacotes CBS com XML pendente. Limitado a
// 5000 subchaves para nao custar caro em maquinas com muitos pacotes
// (medido: 6188 pacotes nesta maquina).
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

// windowsUpdateRebootRequiredAfterInstall le
// ...\Results\Install\LastSuccessTime e o flag de reboot do AU: quando o
// Windows acabou de instalar uma KB mas o reboot ainda nao ocorreu, o AU grava
// RebootRequired=1 em \Auto Update\RebootRequired (ja coberto) ou deixa o
// "PostRebootReporting" pendente. A leitura aqui e apenas complementar e nao
// gera falso positivo: exige chave RebootRequired=1 explicita.
func windowsUpdateRebootRequiredAfterInstall() bool {
	if registryDWORDValue(regPathWindowsUpdateLog, "RebootRequired") == 1 {
		return true
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
		regPathWUPolicies,
		regPathWUAutoUpdate,
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
