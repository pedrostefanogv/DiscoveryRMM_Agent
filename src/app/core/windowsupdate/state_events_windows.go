//go:build windows

package windowsupdate

// Leitura MINIMA do log de eventos "Microsoft-Windows-WindowsUpdateClient/
// Operational" via wevtapi (EvtQuery/EvtNext/EvtRender).
//
// Por que nao WindowsUpdate.log: desde o Windows 10 1903 o texto em
// C:\Windows\Logs\WindowsUpdate\WindowsUpdate.log nao e mais escrito — so
// restam .etl binarios. O log de eventos e a fonte real dos eventos 43/44
// ("Instalacao Bem-sucedida" / "Atualizacao baixada") usada pela UI do Windows.
//
// Custo: a leitura e limitada (EvtQuery com direcao "mais recentes primeiro"
// e no maximo N eventos), alguns milissegundos — apropriada para a sonda de
// 5 min. Nada de COM.

import (
	"errors"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	windowsUpdateEventChannel = "Microsoft-Windows-WindowsUpdateClient/Operational"
	// Quantidade de eventos mais recentes lidos por sonda.
	windowsUpdateEventMaxResults = 64

	evtQueryChannelPath     = 0x1
	evtQueryReverseDirection = 0x100

	evtRenderEventXML = 1

	windowsUpdateEventInstallSuccess  = 43
	windowsUpdateEventDownloadStarted = 41
)

var (
	wevtapiDLL            = syscall.NewLazyDLL("wevtapi.dll")
	procEvtQuery          = wevtapiDLL.NewProc("EvtQuery")
	procEvtNext           = wevtapiDLL.NewProc("EvtNext")
	procEvtClose          = wevtapiDLL.NewProc("EvtClose")
	procEvtRender         = wevtapiDLL.NewProc("EvtRender")

	windowsUpdateEventCacheMu sync.Mutex
	windowsUpdateEventCache   struct {
		at   time.Time
		info WindowsUpdateLogActivity
	}
)

// windowsUpdateEventsInterval controla o cache da leitura do log de eventos: a
// sonda roda a cada 5 min, nao ha razao para consultar o wevtapi em todo scan.
const windowsUpdateEventsInterval = 45 * time.Second

// probeWindowsUpdateEvents le os eventos recentes do WindowsUpdateClient e
// resume instalacoes e downloads. Nunca panica: qualquer falha devolve
// Read=false (a sonda continua com o resto).
func probeWindowsUpdateEvents() WindowsUpdateLogActivity {
	windowsUpdateEventCacheMu.Lock()
	if !windowsUpdateEventCache.at.IsZero() && time.Since(windowsUpdateEventCache.at) < windowsUpdateEventsInterval {
		cached := windowsUpdateEventCache.info
		windowsUpdateEventCacheMu.Unlock()
		return cached
	}
	windowsUpdateEventCacheMu.Unlock()

	info := readWindowsUpdateEvents()
	windowsUpdateEventCacheMu.Lock()
	windowsUpdateEventCache.at = time.Now()
	windowsUpdateEventCache.info = info
	windowsUpdateEventCacheMu.Unlock()
	return info
}

// WindowsUpdateEventRecord e um evento bruto parseado do XML do wevtapi.
type WindowsUpdateEventRecord struct {
	EventID int
	Time    *time.Time
	Message string
}

// ReadWindowsUpdateEvents le os N eventos mais recentes do canal (para
// diagnostico/teste; a sonda usa probeWindowsUpdateEvents com cache).
func ReadWindowsUpdateEvents() ([]WindowsUpdateEventRecord, error) {
	channelPtr, err := syscall.UTF16PtrFromString(windowsUpdateEventChannel)
	if err != nil {
		return nil, err
	}
	query, _, callErr := procEvtQuery.Call(0, uintptr(unsafe.Pointer(channelPtr)), 0, uintptr(evtQueryChannelPath|evtQueryReverseDirection))
	if query == 0 {
		return nil, errors.New("EvtQuery falhou: " + callErr.Error())
	}
	defer procEvtClose.Call(query)

	handles := make([]syscall.Handle, windowsUpdateEventMaxResults)
	var returned uint32
	ok, _, callErr := procEvtNext.Call(
		query,
		uintptr(len(handles)),
		uintptr(unsafe.Pointer(&handles[0])),
		0,
		0,
		uintptr(unsafe.Pointer(&returned)),
	)
	if ok == 0 || returned == 0 {
		// Nenhum evento e um resultado valido (canal vazio).
		if callErrno, okErr := callErr.(syscall.Errno); okErr && callErrno == 259 /* ERROR_NO_MORE_ITEMS */ {
			return nil, nil
		}
		return nil, nil
	}
	records := make([]WindowsUpdateEventRecord, 0, returned)
	for i := uint32(0); i < returned; i++ {
		handle := handles[i]
		if xml, err := renderEventXML(handle); err == nil {
			if record, ok := parseWindowsUpdateEventXML(xml); ok {
				records = append(records, record)
			}
		}
		procEvtClose.Call(uintptr(handle))
	}
	return records, nil
}

func renderEventXML(handle syscall.Handle) (string, error) {
	var bufferSize, used, props uint32
	ok, _, _ := procEvtRender.Call(0, uintptr(handle), uintptr(evtRenderEventXML), 0, 0, uintptr(unsafe.Pointer(&bufferSize)), uintptr(unsafe.Pointer(&props)))
	if ok != 0 || bufferSize == 0 {
		return "", errors.New("EvtRender sem tamanho")
	}
	buffer := make([]uint16, bufferSize/2+1)
	ok, _, callErr := procEvtRender.Call(0, uintptr(handle), uintptr(evtRenderEventXML), uintptr(bufferSize), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&props)))
	if ok == 0 {
		return "", errors.New("EvtRender falhou: " + callErr.Error())
	}
	return syscall.UTF16ToString(buffer), nil
}

// ParseWindowsUpdateEventXML extrai SystemTime/EventID e o texto do evento do
// XML do wevtapi.
func parseWindowsUpdateEventXML(xml string) (WindowsUpdateEventRecord, bool) {
	record := WindowsUpdateEventRecord{}
	if value := xmlAttribute(xml, "SystemTime"); value != "" {
		if stamp := windowsUpdateLogTimestamp(value); stamp != nil {
			record.Time = stamp
		}
	}
	if value := xmlElement(xml, "EventID"); value != "" {
		parsed := 0
		for _, r := range value {
			if r < '0' || r > '9' {
				break
			}
			parsed = parsed*10 + int(r-'0')
		}
		record.EventID = parsed
	}
	record.Message = strings.TrimSpace(xmlElement(xml, "Message"))
	return record, record.EventID != 0
}

// xmlAttribute procura name="valor" tolerando prefixo de namespace
// (xsi:SystemTime, Event_SystemTime...). Evita dependencia de um parser XML
// completo para tres campos.
func xmlAttribute(xml, name string) string {
	for offset := 0; ; {
		idx := strings.Index(xml[offset:], name+"=")
		if idx < 0 {
			return ""
		}
		position := offset + idx
		// Fim do nome do atributo exato (evita casar "InstallTime=" ao procurar
		// "Time="): o caractere anterior nao pode ser letra/digito.
		if position > 0 {
			previous := xml[position-1]
			exact := !isXMLNameChar(previous) || previous == ':'
			if !exact {
				offset = position + len(name) + 1
				continue
			}
		}
		rest := xml[position+len(name)+1:]
		if len(rest) == 0 {
			return ""
		}
		quote := rest[0]
		if quote != '"' && quote != '\'' {
			return ""
		}
		rest = rest[1:]
		end := strings.IndexByte(rest, quote)
		if end < 0 {
			return ""
		}
		return rest[:end]
	}
}

func isXMLNameChar(value byte) bool {
	return value == '_' || value == '-' || value == '.' ||
		(value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9')
}

func xmlElement(xml, name string) string {
	if value := xmlElementExact(xml, name); value != "" {
		return value
	}
	openPrefix := "<"
	start := strings.Index(xml, openPrefix)
	_ = start
	// Suporta <ns:name>...</ns:name>.
	occurrence := strings.Index(xml, ":"+name+">")
	if occurrence < 0 {
		return ""
	}
	rest := xml[occurrence+len(name)+2:]
	end := strings.Index(rest, ":"+name+">")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

func xmlElementExact(xml, name string) string {
	open := "<" + name + ">"
	closeTag := "</" + name + ">"
	start := strings.Index(xml, open)
	if start < 0 {
		return ""
	}
	rest := xml[start+len(open):]
	end := strings.Index(rest, closeTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// readWindowsUpdateEvents produz o mesmo resumo da sonda de log textual.
func readWindowsUpdateEvents() WindowsUpdateLogActivity {
	records, err := ReadWindowsUpdateEvents()
	if err != nil || len(records) == 0 {
		return WindowsUpdateLogActivity{}
	}
	return SummarizeWindowsUpdateEvents(records)
}

// SummarizeWindowsUpdateEvents converte os eventos em WindowsUpdateLogActivity
// (logica pura, testavel sem wevtapi):
//   - 43 (instalacao bem-sucedida) => LastInstallSuccess e LastInstallTime;
//   - 41 (atualizacao baixada) / instalacao => LastInstallTime (muda a cada
//     instalacao, mesmo quando falha depois);
//   - download (41) MAIS RECENTE que a ultima instalacao => DownloadInProgress
//     (o Windows baixou e ainda nao instalou/rebootou).
func SummarizeWindowsUpdateEvents(records []WindowsUpdateEventRecord) WindowsUpdateLogActivity {
	activity := WindowsUpdateLogActivity{Read: true, LastDownloadProgress: -1}
	var lastDownload *time.Time
	for _, record := range records {
		if record.Time == nil {
			continue
		}
		stamp := *record.Time
		switch record.EventID {
		case windowsUpdateEventInstallSuccess:
			if activity.LastInstallSuccess == nil || stamp.After(*activity.LastInstallSuccess) {
				copied := stamp
				activity.LastInstallSuccess = &copied
			}
			if activity.LastInstallTime == nil || stamp.After(*activity.LastInstallTime) {
				copied := stamp
				activity.LastInstallTime = &copied
			}
		case windowsUpdateEventDownloadStarted:
			if lastDownload == nil || stamp.After(*lastDownload) {
				copied := stamp
				lastDownload = &copied
			}
			if activity.LastInstallTime == nil || stamp.After(*activity.LastInstallTime) {
				copied := stamp
				activity.LastInstallTime = &copied
			}
		default:
			// Texto do evento como reforco (ids 43/44 mudam entre versoes, o
			// texto da mensagem nao). "reinicio necessario" NAO conta como
			// sucesso: instalacao que pede reboot ainda aparece como baixada.
			message := strings.ToLower(strings.TrimSpace(record.Message))
			successful := (strings.Contains(message, "bem-sucedida") || strings.Contains(message, "successful") ||
				strings.Contains(message, "concluida com exito")) && !strings.Contains(message, "reinicio")
			if successful {
				if activity.LastInstallSuccess == nil || stamp.After(*activity.LastInstallSuccess) {
					copied := stamp
					activity.LastInstallSuccess = &copied
				}
				if activity.LastInstallTime == nil || stamp.After(*activity.LastInstallTime) {
					copied := stamp
					activity.LastInstallTime = &copied
				}
			}
			if strings.Contains(message, "baixad") || strings.Contains(message, "download") {
				if lastDownload == nil || stamp.After(*lastDownload) {
					copied := stamp
					lastDownload = &copied
				}
				if activity.LastInstallTime == nil || stamp.After(*activity.LastInstallTime) {
					copied := stamp
					activity.LastInstallTime = &copied
				}
			}
		}
	}
	if lastDownload != nil {
		// Download mais recente que a ultima INSTALACAO BEM-SUCEDIDA: o pacote
		// esta no disco aguardando instalacao/reboot — estado que a UI precisa
		// mostrar e que NAO muda a lista de KBs pendentes do WUA.
		activity.DownloadInProgress = activity.LastInstallSuccess == nil || lastDownload.After(*activity.LastInstallSuccess)
	}
	return activity
}
