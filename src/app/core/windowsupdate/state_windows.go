//go:build windows

package windowsupdate

// Sonda BARATA do estado do Windows Update (sem WUA, sem WMI, sem busca).
//
// Tudo aqui e leitura de registro, consulta ao SCM e parse do log de
// instalacao (WindowsUpdate.log) — custo total de poucos milissegundos, o que
// permite rodar a cada ~5 min. O resultado alimenta WindowsUpdateStateSignature:
// quando a assinatura muda, o agente dispara um scan completo do WUA.

import (
	"bytes"
	"encoding/binary"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

const (
	// windowsUpdateLogPath e o log textual dos eventos do WUClient (o mesmo
	// fonte do Visualizador de Eventos "WindowsUpdateClient").
	windowsUpdateLogPath = `C:WindowsLogsWindowsUpdateWindowsUpdate.log`
	// windowsUpdateLogMaxBytes limita a leitura (log pode passar de 10 MB).
	windowsUpdateLogMaxBytes = 4 << 20
	// lastSuccessTimeMaxAge acima disso nao e "wuOnline" (maquina parada ha dias).
	lastSuccessTimeMaxAge = 7 * 24 * time.Hour
	// logActivityWindow: janela para considerar o WUClient "online" (baixando
	// ou instalando agora).
	logActivityWindow = 30 * time.Minute
)

// WindowsUpdateLogActivity resume o que o WindowsUpdate.log mostra.
type WindowsUpdateLogActivity struct {
	Read               bool
	LastInstallTime    *time.Time
	LastInstallSuccess *time.Time
	DownloadInProgress bool
	InstallInProgress  bool
	// LastDownloadProgress e a ultima porcentagem vista (0-100), -1 quando
	// nao ha download no trecho lido.
	LastDownloadProgress int
}

// ProbeLocalState monta o estado barato do Windows Update. Nao executa WUA:
// pode (e deve) ser chamado com frequencia.
func ProbeLocalState() WindowsUpdateState {
	now := time.Now().UTC()
	state := WindowsUpdateState{
		Supported:    true,
		ServiceState: windowsUpdateServiceState(),
		CheckedAt:    now,
	}

	if wsus := wsusServerFromPolicy(); wsus != "" {
		state.WSUSServer = wsus
		state.PolicyManaged = true
	}
	state.AuOptions, state.NoAutoUpdate, state.DeferralDays, state.ActiveHoursStart,
		state.ActiveHoursEnd, state.PolicyErrorCode = readWindowsUpdatePolicySnapshot()

	reboot := probePendingReboot()
	state.RebootPending = reboot.Pending
	state.RebootReasons = append([]string(nil), reboot.Reasons...)

	// Fonte primaria: log de eventos do WindowsUpdateClient (o log textual foi
	// descontinuado no Windows 10 1903+). Fallback: WindowsUpdate.log se existir.
	activity := probeWindowsUpdateEvents()
	if !activity.Read || (activity.LastInstallTime == nil && activity.LastInstallSuccess == nil) {
		if textActivity := readWindowsUpdateLogActivity(); textActivity.Read {
			activity = mergeWindowsUpdateActivity(activity, textActivity)
		}
	}
	applyWindowsUpdateLogActivity(&state, activity)

	qfes := installedKBsFromRegistry()
	state.QFEInstalledCount = len(qfes)
	if len(qfes) > 8 {
		state.RecentQFEs = append([]string(nil), qfes[:8]...)
	} else {
		state.RecentQFEs = qfes
	}

	if strings.EqualFold(state.ServiceState, "disabled") {
		state.PolicyErrorCode = ScanErrorServiceDisabled
	}

	// WuOnline: log com atividade recente ou LastSuccessTime recente.
	if state.LastInstallTime != nil && now.Sub(*state.LastInstallTime) <= lastSuccessTimeMaxAge {
		state.WuOnline = true
	}
	if state.LastInstallSuccess != nil && now.Sub(*state.LastInstallSuccess) <= lastSuccessTimeMaxAge {
		state.WuOnline = true
	}
	return state
}

// readWindowsUpdatePolicySnapshot le a politica efetiva do WU (AUOptions,
// deferrals, active hours). Todos os valores sao opcionais: zero = ausente.
func readWindowsUpdatePolicySnapshot() (auOptions int, noAutoUpdate bool, deferralDays int, activeStart, activeEnd *int, errorCode string) {
	paths := []string{regPathWUPolicies, regPathWUAutoUpdate}

	readDWORDAny := func(name string) (uint64, bool) {
		for _, path := range paths {
			key, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			value, _, err := key.GetIntegerValue(name)
			key.Close()
			if err == nil {
				return value, true
			}
		}
		return 0, false
	}

	if value, ok := readDWORDAny("AUOptions"); ok {
		auOptions = int(value)
	}
	if value, ok := readDWORDAny("NoAutoUpdate"); ok && value == 1 {
		noAutoUpdate = true
	}
	if value, ok := readDWORDAny("DeferQualityUpdatesPeriodInDays"); ok {
		deferralDays = int(value)
	}
	if value, ok := readDWORDAny("BizConnected"); ok && value == 1 && deferralDays == 0 {
		if deferral, ok := readDWORDAny("DeferFeatureUpdatesPeriodInDays"); ok {
			deferralDays = int(deferral)
		}
	}
	start, hasStart := readDWORDAny("ActiveHoursStart")
	end, hasEnd := readDWORDAny("ActiveHoursEnd")
	if hasStart && start <= 23 {
		value := int(start)
		activeStart = &value
	}
	if hasEnd && end <= 23 {
		value := int(end)
		activeEnd = &value
	}
	return auOptions, noAutoUpdate, deferralDays, activeStart, activeEnd, errorCode
}

// installedKBsFromRegistry lista as KBs instaladas via
// ...\Component Based Servicing\Packages\Package_for_KBxxxxx~... — barato
// (uma subchave por pacote) e sem WMI. Retorna KBs ordenadas (maior primeiro).
func installedKBsFromRegistry() []string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, regPathCBSPackages, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil
	}
	defer key.Close()
	names, err := key.ReadSubKeyNames(5000)
	if err != nil && len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		kb := kbFromServicingPackageName(name)
		if kb == "" {
			continue
		}
		if _, ok := seen[kb]; ok {
			continue
		}
		seen[kb] = struct{}{}
		out = append(out, kb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] > out[j] })
	return out
}

// kbFromServicingPackageName extrai o KB de um nome de pacote CBS:
// "Package_for_KB5127690~31bf3856ad364e35~amd64~~26120.1.1.0" => "KB5127690".
func kbFromServicingPackageName(name string) string {
	trimmed := strings.TrimSpace(name)
	upper := strings.ToUpper(trimmed)
	idx := strings.Index(upper, "FOR_KB")
	if idx < 0 {
		return ""
	}
	rest := trimmed[idx+len("FOR_KB"):]
	digits := 0
	for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
		digits++
	}
	if digits < 4 {
		return ""
	}
	return NormalizeKB(rest[:digits])
}

// readWindowsUpdateLogActivity faz o parse do WindowsUpdate.log (UTF-8 ou
// UTF-16LE; o arquivo e rotacionado no boot).
func readWindowsUpdateLogActivity() WindowsUpdateLogActivity {
	info, err := os.Stat(windowsUpdateLogPath)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return WindowsUpdateLogActivity{}
	}
	size := info.Size()
	offset := int64(0)
	if size > windowsUpdateLogMaxBytes {
		offset = size - windowsUpdateLogMaxBytes
	}
	file, err := os.Open(windowsUpdateLogPath)
	if err != nil {
		return WindowsUpdateLogActivity{}
	}
	defer file.Close()
	if offset > 0 {
		if _, err := file.Seek(offset, 0); err != nil {
			return WindowsUpdateLogActivity{}
		}
	}
	raw := make([]byte, size-offset)
	read, err := file.Read(raw)
	if err != nil && read == 0 {
		return WindowsUpdateLogActivity{}
	}
	text := decodeWindowsUpdateLog(raw[:read])
	activity := ParseWindowsUpdateLogActivity(text)
	activity.Read = true
	return activity
}

// decodeWindowsUpdateLog detecta UTF-16LE (BOM FF FE) e converte; caso
// contrario usa o texto como UTF-8 (o log nasceu UTF-16 e passou a UTF-8 em
// builds recentes).
func decodeWindowsUpdateLog(raw []byte) string {
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		return decodeUTF16LE(raw[2:])
	}
	// Heuristica: texto UTF-16LE sem BOM (offset no meio do arquivo) tem muitos
	// bytes zero nas posicoes impares.
	zeros := 0
	limit := len(raw)
	if limit > 512 {
		limit = 512
	}
	for i := 1; i < limit; i += 2 {
		if raw[i] == 0 {
			zeros++
		}
	}
	if limit > 8 && zeros > limit/4 {
		if offset := bytes.IndexFunc(raw, func(r rune) bool { return r == 0 }); offset > 0 && offset%2 == 1 {
			raw = raw[offset-1:]
		}
		return decodeUTF16LE(raw)
	}
	return string(raw)
}

func decodeUTF16LE(raw []byte) string {
	count := len(raw) / 2
	units := make([]uint16, 0, count)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(raw[i:]))
	}
	return string(utf16.Decode(units))
}

// ParseWindowsUpdateLogActivity extrai do texto do WindowsUpdate.log:
//   - "Instalacao Bem-sucedida" / "Installation Successful" (evento 43/44 do
//     WUClient) => LastInstallSuccess;
//   - qualquer linha de instalacao/download => LastInstallTime (muda enquanto
//     o Windows instala);
//   - download em andamento ("Baixando - 42%", "Downloading") sem instalacao
//     depois => DownloadInProgress.
//
// A logica nao toca em I/O para ser testavel.
func ParseWindowsUpdateLogActivity(text string) WindowsUpdateLogActivity {
	activity := WindowsUpdateLogActivity{LastDownloadProgress: -1}
	lines := strings.Split(text, "\n")
	var lastDownload *time.Time
	var lastDownloadPercent = -1

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		stamp := windowsUpdateLogTimestamp(trimmed)
		lower := strings.ToLower(trimmed)

		isInstall := strings.Contains(lower, "instalacao") || strings.Contains(lower, "install")
		isDownload := strings.Contains(lower, "baixando") || strings.Contains(lower, "download")

		if isDownload {
			if percent := windowsUpdateLogPercent(lower); percent >= 0 {
				lastDownloadPercent = percent
			}
			if stamp != nil {
				value := *stamp
				if lastDownload == nil || value.After(*lastDownload) {
					lastDownload = &value
				}
			}
		}

		if isInstall && stamp != nil {
			value := *stamp
			if activity.LastInstallTime == nil || value.After(*activity.LastInstallTime) {
				activity.LastInstallTime = &value
			}
			if strings.Contains(lower, "bem-sucedida") || strings.Contains(lower, "successful") ||
				strings.Contains(lower, "concluida com exito") || strings.Contains(lower, "installed successfully") {
				if activity.LastInstallSuccess == nil || value.After(*activity.LastInstallSuccess) {
					copied := value
					activity.LastInstallSuccess = &copied
				}
			}
		}
	}

	activity.LastDownloadProgress = lastDownloadPercent
	if lastDownloadPercent >= 0 && lastDownloadPercent < 100 {
		// Download anunciado e sem instalacao posterior => ainda baixando.
		if activity.LastInstallTime == nil || lastDownload == nil || activity.LastInstallTime.Before(*lastDownload) {
			activity.DownloadInProgress = true
		}
	}
	return activity
}

// mergeWindowsUpdateActivity combina duas fontes: vence o timestamp mais
// recente e DownloadInProgress se qualquer fonte indicar download pendente.
func mergeWindowsUpdateActivity(primary, secondary WindowsUpdateLogActivity) WindowsUpdateLogActivity {
	merged := primary
	merged.Read = primary.Read || secondary.Read
	if newerTime(secondary.LastInstallTime, merged.LastInstallTime) {
		merged.LastInstallTime = secondary.LastInstallTime
	}
	if newerTime(secondary.LastInstallSuccess, merged.LastInstallSuccess) {
		merged.LastInstallSuccess = secondary.LastInstallSuccess
	}
	if secondary.DownloadInProgress {
		merged.DownloadInProgress = true
	}
	if secondary.LastDownloadProgress > merged.LastDownloadProgress {
		merged.LastDownloadProgress = secondary.LastDownloadProgress
	}
	return merged
}

func newerTime(candidate, current *time.Time) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	return candidate.After(*current)
}

func applyWindowsUpdateLogActivity(state *WindowsUpdateState, activity WindowsUpdateLogActivity) {
	if state == nil {
		return
	}
	state.LastInstallTime = activity.LastInstallTime
	state.LastInstallSuccess = activity.LastInstallSuccess
	state.DownloadInProgress = activity.DownloadInProgress
}

// windowsUpdateLogTimestamp extrai o timestamp do inicio da linha do log.
// Formatos aceitos:
//
//	2026-10-16 21:09:57.1234567   (WUClient)
//	2026/10/16 21:09:57           (log antigo)
//	2026-10-16T21:09:57Z          (ETW)
func windowsUpdateLogTimestamp(line string) *time.Time {
	if len(line) < 10 {
		return nil
	}
	candidate := strings.ReplaceAll(line[:19], "/", "-")
	candidate = strings.ReplaceAll(candidate, "T", " ")
	layouts := []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, candidate); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

func windowsUpdateLogPercent(lower string) int {
	idx := strings.Index(lower, "%")
	if idx < 0 {
		return -1
	}
	start := idx - 1
	for start >= 0 && lower[start] >= '0' && lower[start] <= '9' {
		start--
	}
	digits := strings.TrimSpace(lower[start+1 : idx])
	if digits == "" || len(digits) > 3 {
		return -1
	}
	value, err := strconv.Atoi(digits)
	if err != nil || value < 0 || value > 100 {
		return -1
	}
	return value
}
