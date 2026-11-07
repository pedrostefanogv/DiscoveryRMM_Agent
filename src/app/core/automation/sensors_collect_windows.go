//go:build windows

package automation

// Coletores NATIVOS de sensores (Windows).
//
// Tudo aqui roda como a identidade do AGENTE (SYSTEM no modo serviço) e evita
// subprocesso quando existe API nativa: o motor roda a cada ~15s e um spawn por
// sensor custaria CPU/RAM em máquina fraca. As exceções (ping sem raw socket,
// SCM sem handle) caem em binário do próprio Windows, com parsing defensivo.
//
// Nenhum limiar é avaliado aqui: o coletor devolve EVIDÊNCIA e o servidor
// decide status/severidade.

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"discovery/app/core/fileserver"
	"discovery/app/core/platform"
	"discovery/app/core/processutil"
)

// collectSensor coleta UM sensor e devolve 0..N amostras (uma por alvo).
func collectSensor(ctx context.Context, cfg *SensorConfig) []sensorCollection {
	if cfg == nil {
		return []sensorCollection{{Err: "sensor invalido"}}
	}
	switch cfg.Policy.SensorType {
	case SensorTypeDiskSpace:
		return collectDiskSpace(cfg)
	case SensorTypeCpu:
		return []sensorCollection{collectCPUSample(ctx)}
	case SensorTypeMemory:
		return []sensorCollection{collectMemorySample()}
	case SensorTypeUptime:
		return []sensorCollection{collectUptimeSample()}
	case SensorTypeService:
		return []sensorCollection{collectServiceSample(ctx, cfg)}
	case SensorTypeProcess:
		return []sensorCollection{collectProcessSample(cfg)}
	case SensorTypePing:
		return []sensorCollection{collectPingSample(ctx, cfg)}
	case SensorTypeTcpPort:
		return []sensorCollection{collectTCPPortSample(ctx, cfg)}
	case SensorTypeHTTPProbe:
		return []sensorCollection{collectHTTPProbeSample(ctx, cfg)}
	case SensorTypeDNSLatency:
		// 1 amostra (targetKey vazio): DNS não tem "todos os alvos" como o disco.
		return []sensorCollection{collectDNSLatencySample(ctx, cfg)}
	case SensorTypeHTTPThroughput:
		return []sensorCollection{collectHTTPThroughputSample(ctx, cfg)}
	case SensorTypeScript:
		return []sensorCollection{runSensorScript(ctx, cfg)}
	case SensorTypeBandwidth:
		return []sensorCollection{collectBandwidthSample(ctx, cfg)}
	case SensorTypeDiskIO:
		return []sensorCollection{collectDiskIOSample(ctx, cfg)}
	case SensorTypeEventLogRate:
		return []sensorCollection{collectEventLogRateSample(ctx, cfg)}
	case SensorTypeSmartStatus:
		return collectSmartStatusSamples(ctx, cfg)
	case SensorTypeCertificate:
		// Dois caminhos de evidência, escolhidos pelo ALVO (F6):
		//   - alvo local ("local", "-" ou vazio) → cert store da PRÓPRIA máquina
		//     + bindings HTTPS do IIS;
		//   - qualquer outro (host / host:porta) → endpoint TLS remoto, que é o
		//     comportamento original e continua sendo o default quando há alvo.
		//
		// O alvo vazio antes caía no caminho remoto e virava erro "sensor de
		// certificado sem alvo". Com o store local ele passa a ter um significado
		// útil (a própria máquina), SEM alterar quem já configura host/porta: a
		// detecção exige um marcador explícito, então "10.0.0.5" e "host:443"
		// seguem idênticos ao que eram.
		if alvoEhCertStoreLocal(cfg.Policy.Target) {
			return []sensorCollection{collectLocalCertificateSample(ctx, cfg)}
		}
		return []sensorCollection{collectCertificateExpirySample(ctx, cfg)}
	case SensorTypeDefenderStatus:
		return []sensorCollection{collectDefenderSample(ctx)}
	case SensorTypeBitLocker:
		return collectBitLockerSamples(cfg)
	case SensorTypeADConsistency:
		// F5: 1 amostra (targetKey vazio) — a consistência é do DC inteiro, não
		// de um alvo por linha. Em cliente/servidor membro o coletor devolve
		// coleta vazia SEM erro (não é falha, o sensor não se aplica).
		return []sensorCollection{CollectADConsistency(ctx, cfg)}
	default:
		return []sensorCollection{{Err: fmt.Sprintf("sensor tipo %q nao suportado pelo agente", cfg.Policy.SensorType)}}
	}
}

// roundSensorPercent arredonda percentuais para 2 casas: payload menor e
// gráfico sem ruído de ponto flutuante.
func roundSensorPercent(value float64) float64 {
	return math.Round(value*100) / 100
}

// ── Disco ──

// collectDiskSpace devolve uma amostra POR VOLUME. target vazio = todos os
// volumes fixos; target "C:" = só aquele volume.
func collectDiskSpace(cfg *SensorConfig) []sensorCollection {
	target := strings.TrimSpace(cfg.Policy.Target)
	drives := fileserver.ListDrives()
	selected := make([]fileserver.DriveInfo, 0, len(drives))

	if target == "" {
		for _, drive := range drives {
			if drive.Type == "fixed" {
				selected = append(selected, drive)
			}
		}
	} else {
		normalized := normalizeDriveTarget(target)
		for _, drive := range drives {
			if strings.EqualFold(strings.TrimSpace(drive.Name), normalized) {
				selected = append(selected, drive)
				break
			}
		}
		if len(selected) == 0 {
			// Volume configurado não existe no host: medida VAZIA (o servidor
			// decide Failing "volume nao encontrado"). Não é erro de coleta.
			return []sensorCollection{{TargetKey: normalized}}
		}
	}

	if len(selected) == 0 {
		return []sensorCollection{{Err: "nenhum volume fixo encontrado no host"}}
	}

	out := make([]sensorCollection, 0, len(selected))
	for _, drive := range selected {
		collection := sensorCollection{TargetKey: drive.Name}
		if !drive.Ready || drive.TotalBytes <= 0 {
			collection.Err = fmt.Sprintf("volume %s sem midia ou sem espaco reportado", drive.Name)
			out = append(out, collection)
			continue
		}
		freeBytes := float64(drive.FreeBytes)
		totalBytes := float64(drive.TotalBytes)
		freePercent := freeBytes * 100 / totalBytes
		collection.Measure = SensorMeasure{
			FreePercent: sensorFloat(roundSensorPercent(freePercent)),
			UsedPercent: sensorFloat(roundSensorPercent(100 - freePercent)),
			TotalBytes:  sensorFloat(totalBytes),
			FreeBytes:   sensorFloat(freeBytes),
		}
		out = append(out, collection)
	}
	return out
}

// normalizeDriveTarget aceita "c:", "C", "c:\" e devolve "C:".
func normalizeDriveTarget(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimRight(trimmed, "\\/")
	if len(trimmed) == 1 {
		return strings.ToUpper(trimmed) + ":"
	}
	return strings.ToUpper(trimmed)
}

// ── CPU / memória / uptime ──

func collectCPUSample(ctx context.Context) sensorCollection {
	sampler := platform.NewCPUSampler()
	used := sampler.Sample()
	if used < 0 {
		// Primeira amostra só cria a baseline (delta de GetSystemTimes): espera
		// um instante e amostra de novo.
		select {
		case <-ctx.Done():
			return sensorCollection{Measure: SensorMeasure{TimedOut: true}, Err: "tempo esgotado ao amostrar a CPU"}
		case <-time.After(250 * time.Millisecond):
		}
		used = sampler.Sample()
	}
	if used < 0 {
		return sensorCollection{Err: "nao foi possivel amostrar o uso de CPU"}
	}
	return sensorCollection{Measure: SensorMeasure{UsedPercent: sensorFloat(roundSensorPercent(used))}}
}

func collectMemorySample() sensorCollection {
	measure := SensorMeasure{}
	usedPercent := platform.SampleMemoryPercent()
	if usedPercent >= 0 {
		measure.UsedPercent = sensorFloat(roundSensorPercent(usedPercent))
	}
	if totalBytes, availableBytes, ok := sensorMemoryStatus(); ok && totalBytes > 0 {
		measure.TotalBytes = sensorFloat(float64(totalBytes))
		measure.FreeBytes = sensorFloat(float64(availableBytes))
		if measure.UsedPercent == nil {
			used := float64(totalBytes - availableBytes)
			measure.UsedPercent = sensorFloat(roundSensorPercent(used * 100 / float64(totalBytes)))
		}
	}
	if measure.UsedPercent == nil {
		return sensorCollection{Err: "nao foi possivel amostrar a memoria"}
	}
	return sensorCollection{Measure: measure}
}

func collectUptimeSample() sensorCollection {
	milliseconds, ok := getSystemUptimeMilliseconds()
	if !ok {
		return sensorCollection{Err: "nao foi possivel ler o tempo de atividade do host"}
	}
	seconds := float64(milliseconds) / 1000
	return sensorCollection{Measure: SensorMeasure{UptimeSeconds: sensorFloat(math.Round(seconds*10) / 10)}}
}

var (
	modkernel32Sensors       = syscall.NewLazyDLL("kernel32.dll")
	procGetTickCount64       = modkernel32Sensors.NewProc("GetTickCount64")
	procGlobalMemoryStatusEx = modkernel32Sensors.NewProc("GlobalMemoryStatusEx")
	modpsapiSensors          = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapiSensors.NewProc("GetProcessMemoryInfo")
)

// sensorMemoryStatusEx espelha MEMORYSTATUSEX. Duplicado de propósito: o pacote
// platform só exporta o PERCENTUAL (SampleMemoryPercent), e o contrato do sensor
// quer TotalBytes/FreeBytes da memória.
type sensorMemoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// sensorMemoryStatus devolve (total, disponivel) em bytes.
func sensorMemoryStatus() (uint64, uint64, bool) {
	var status sensorMemoryStatusEx
	status.length = uint32(unsafe.Sizeof(status))
	result, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if result == 0 || status.totalPhys == 0 {
		return 0, 0, false
	}
	return status.totalPhys, status.availPhys, true
}

// getSystemUptimeMilliseconds lê GetTickCount64 (ms desde o boot).
func getSystemUptimeMilliseconds() (uint64, bool) {
	result, _, _ := procGetTickCount64.Call()
	if result == 0 {
		return 0, false
	}
	return uint64(result), true
}

// ── Serviço (SCM) ──

// serviceStateFor mapeia svc.State para o vocabulário do contrato
// ("Running|Stopped|StartPending|NotFound"). Estados intermediários são
// colapsados de propósito: o servidor só distingue Running/StartPending.
func serviceStateFor(state svc.State) (string, bool, error) {
	switch state {
	case svc.Running:
		return "Running", true, nil
	case svc.StartPending, svc.ContinuePending:
		return "StartPending", false, nil
	default:
		return "Stopped", false, nil
	}
}

func collectServiceSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	name := strings.TrimSpace(cfg.Policy.Target)
	if name == "" {
		return sensorCollection{Err: "sensor de servico sem alvo (nome do servico)"}
	}
	state, running, err := queryServiceState(ctx, name)
	if err != nil {
		return sensorCollection{Err: err.Error()}
	}
	// restartIfStopped: serviço parado pode ser reiniciado pelo agente antes de
	// reportar. Só Stopped tenta; StartPending/Running/NotFound não.
	if !running && state == "Stopped" && cfg.Options.Bool(false, "restartIfStopped", "restart", "autoRestart") {
		return restartStoppedServiceWith(ctx, cfg, name, scmSensorServiceController{})
	}
	return sensorCollection{Measure: SensorMeasure{
		ServiceState: sensorString(state),
		Running:      sensorBool(running),
	}}
}

// queryServiceState consulta o SCM nativo e, se a API falhar (handle negado),
// cai no "sc query".
func queryServiceState(ctx context.Context, name string) (string, bool, error) {
	manager, err := mgr.Connect()
	if err == nil {
		defer manager.Disconnect()
		service, openErr := manager.OpenService(name)
		if openErr == nil {
			defer service.Close()
			status, queryErr := service.Query()
			if queryErr == nil {
				return serviceStateFor(status.State)
			}
		} else if errors.Is(openErr, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return "NotFound", false, nil
		}
	}
	return queryServiceStateWithSC(ctx, name)
}

var serviceStateSCPattern = regexp.MustCompile("(?mi)STATE\\s*:\\s*(\\d+)\\s+([A-Z_]+)")

// queryServiceStateWithSC é o fallback via "sc query" (mesmo dado do SCM).
func queryServiceStateWithSC(ctx context.Context, name string) (string, bool, error) {
	output, err := runHiddenCommand(ctx, 10*time.Second, "sc", "query", name)
	lowered := strings.ToLower(output)
	if strings.Contains(lowered, "1060") || strings.Contains(lowered, "does not exist") || strings.Contains(lowered, "nao existe") {
		return "NotFound", false, nil
	}
	match := serviceStateSCPattern.FindStringSubmatch(output)
	if len(match) < 3 {
		if err != nil {
			return "", false, fmt.Errorf("falha ao consultar o servico %q: %v", name, err)
		}
		return "", false, fmt.Errorf("resposta inesperada do sc query para o servico %q", name)
	}
	code, _ := strconv.Atoi(match[1])
	switch code {
	case 4: // SERVICE_RUNNING
		return "Running", true, nil
	case 2, 5: // START_PENDING / CONTINUE_PENDING
		return "StartPending", false, nil
	default:
		return "Stopped", false, nil
	}
}

// ── Processo ──

// sensorProcessMemoryCounters espelha PROCESS_MEMORY_COUNTERS (psapi).
type sensorProcessMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

// processWorkingSetBytes lê o working set de um processo (0 quando sem acesso).
func processWorkingSetBytes(processID uint32) uint64 {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, processID)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(handle)
	var counters sensorProcessMemoryCounters
	counters.cb = uint32(unsafe.Sizeof(counters))
	result, _, _ := procGetProcessMemoryInfo.Call(uintptr(handle), uintptr(unsafe.Pointer(&counters)), uintptr(counters.cb))
	if result == 0 {
		return 0
	}
	return uint64(counters.workingSetSize)
}

func collectProcessSample(cfg *SensorConfig) sensorCollection {
	target := strings.TrimSpace(cfg.Policy.Target)
	if target == "" {
		return sensorCollection{Err: "sensor de processo sem alvo (nome do executavel)"}
	}
	// "chrome.exe" e "chrome" apontam para o mesmo processo.
	wanted := strings.ToLower(strings.TrimSuffix(target, ".exe"))

	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return sensorCollection{Err: fmt.Sprintf("falha ao enumerar processos: %v", err)}
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	count := 0
	workingSet := uint64(0)
	for nextErr := windows.Process32First(snapshot, &entry); nextErr == nil; nextErr = windows.Process32Next(snapshot, &entry) {
		executable := strings.ToLower(strings.TrimSuffix(syscall.UTF16ToString(entry.ExeFile[:]), ".exe"))
		if executable != wanted {
			continue
		}
		count++
		workingSet += processWorkingSetBytes(entry.ProcessID)
	}

	state := "Stopped"
	if count > 0 {
		state = "Running"
	}
	measure := SensorMeasure{
		Running:      sensorBool(count > 0),
		ProcessCount: sensorInt(count),
		ProcessState: sensorString(state),
	}
	if count > 0 && workingSet > 0 {
		measure.WorkingSetBytes = sensorFloat(float64(workingSet))
	}
	return sensorCollection{Measure: measure}
}

// ── Ping ──

func collectPingSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	target := strings.TrimSpace(cfg.Policy.Target)
	if target == "" {
		return sensorCollection{Err: "sensor de ping sem alvo"}
	}
	count := cfg.Options.Int(4, "count", "packets", "repetitions")
	if count <= 0 || count > 20 {
		count = 4
	}
	perTryMs := cfg.Options.Int(1000, "timeoutMs", "timeout", "timeoutMillis")
	if perTryMs <= 0 {
		perTryMs = 1000
	}
	perTry := time.Duration(perTryMs) * time.Millisecond
	if perTry > 5*time.Second {
		perTry = 5 * time.Second
	}

	address := resolveSensorIPv4(ctx, target)
	if address == nil {
		return sensorCollection{Err: fmt.Sprintf("nao foi possivel resolver %q em IPv4", target)}
	}

	// Agente roda como SYSTEM: raw socket ICMP é permitido. Sem isso (agente
	// não elevado / política de host), cai no ping.exe.
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return collectPingWithExecutable(ctx, target, count, perTry)
	}
	defer conn.Close()

	identifier := os.Getpid() & 0xffff
	rtts := make([]time.Duration, 0, count)
	sent := 0
	for attempt := 1; attempt <= count; attempt++ {
		if ctx.Err() != nil {
			break
		}
		message := icmp.Message{
			Type: ipv4.ICMPTypeEcho,
			Code: 0,
			Body: &icmp.Echo{
				ID:   identifier,
				Seq:  attempt,
				Data: []byte(fmt.Sprintf("discovery-sensor-%d-%d", identifier, attempt)),
			},
		}
		payload, marshalErr := message.Marshal(nil)
		if marshalErr != nil {
			continue
		}
		deadline := time.Now().Add(perTry)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		_ = conn.SetDeadline(deadline)
		started := time.Now()
		if _, writeErr := conn.WriteTo(payload, &net.IPAddr{IP: address}); writeErr != nil {
			sent++
			continue
		}
		sent++

		buffer := make([]byte, 1500)
		for {
			readBytes, _, readErr := conn.ReadFrom(buffer)
			if readErr != nil {
				break
			}
			reply, parseErr := icmp.ParseMessage(1, buffer[:readBytes])
			if parseErr != nil {
				continue
			}
			echo, ok := reply.Body.(*icmp.Echo)
			if !ok || reply.Type != ipv4.ICMPTypeEchoReply {
				continue
			}
			if echo.ID != identifier || echo.Seq != attempt {
				continue
			}
			rtts = append(rtts, time.Since(started))
			break
		}
	}

	return buildPingCollection(sent, rtts)
}

// icmpTimePattern extrai o RTT do ping.exe (localizado: "tempo=12ms",
// "time=12ms", "tempo<1ms").
var icmpTimePattern = regexp.MustCompile("(?i)(?:time|tempo|tiempo)\\s*[=<]\\s*(\\d+(?:[.,]\\d+)?)\\s*ms")

func collectPingWithExecutable(ctx context.Context, target string, count int, perTry time.Duration) sensorCollection {
	totalTimeout := time.Duration(count)*perTry + 5*time.Second
	output, _ := runHiddenCommand(ctx, totalTimeout, "ping", "-n", strconv.Itoa(count), "-w", strconv.Itoa(int(perTry/time.Millisecond)), target)
	rtts := make([]time.Duration, 0, count)
	for _, match := range icmpTimePattern.FindAllStringSubmatch(output, -1) {
		if len(match) < 2 {
			continue
		}
		value, ok := parseLooseFloat(match[1])
		if !ok {
			continue
		}
		rtts = append(rtts, time.Duration(value*float64(time.Millisecond)))
	}
	if len(rtts) == 0 && strings.TrimSpace(output) == "" {
		return sensorCollection{Err: "falha ao executar ping (nem ICMP nativo nem ping.exe disponiveis)"}
	}
	return buildPingCollection(count, rtts)
}

// buildPingCollection monta a evidência de ping (taxa de perda, RTT, jitter).
func buildPingCollection(sent int, rtts []time.Duration) sensorCollection {
	if sent <= 0 {
		sent = len(rtts)
	}
	if sent == 0 {
		// Nenhum pacote saiu (contexto cancelado/resolução falhou): erro de
		// coleta. Sem isso a evidência vazia seria avaliada como PASSING.
		return sensorCollection{Err: "nenhum pacote ICMP enviado"}
	}
	replies := len(rtts)
	measure := SensorMeasure{
		Sent:    sensorInt(sent),
		Replies: sensorInt(replies),
	}
	if sent > 0 {
		measure.LossPercent = sensorFloat(roundSensorPercent(float64(sent-replies) * 100 / float64(sent)))
	}
	if replies == 0 {
		return sensorCollection{Measure: measure}
	}

	minimum := rtts[0]
	maximum := rtts[0]
	sum := time.Duration(0)
	for _, rtt := range rtts {
		if rtt < minimum {
			minimum = rtt
		}
		if rtt > maximum {
			maximum = rtt
		}
		sum += rtt
	}
	average := sum / time.Duration(replies)

	// Jitter = desvio médio absoluto entre RTTs consecutivos (RFC 3550 simplif.).
	jitter := float64(0)
	if replies > 1 {
		totalDiff := float64(0)
		for index := 1; index < replies; index++ {
			diff := rtts[index] - rtts[index-1]
			if diff < 0 {
				diff = -diff
			}
			totalDiff += float64(diff) / float64(time.Millisecond)
		}
		jitter = totalDiff / float64(replies-1)
	}

	measure.AvgRTTMs = sensorFloat(roundSensorMs(average))
	measure.LatencyMinMs = sensorFloat(roundSensorMs(minimum))
	measure.LatencyMaxMs = sensorFloat(roundSensorMs(maximum))
	measure.JitterMs = sensorFloat(roundSensorPercent(jitter))
	return sensorCollection{Measure: measure}
}

func roundSensorMs(value time.Duration) float64 {
	return math.Round(float64(value)/float64(time.Millisecond)*100) / 100
}

// resolveSensorIPv4 resolve o alvo para IPv4 (o ping nativo é ip4).
func resolveSensorIPv4(ctx context.Context, host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4()
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	for _, address := range addresses {
		if v4 := address.IP.To4(); v4 != nil {
			return v4
		}
	}
	return nil
}

// ── TCP ──

// sensorTCPAddress monta host:porta. Porta vem do alvo ("host:443") ou do
// OptionsJson ({"port": 443}).
func sensorTCPAddress(cfg *SensorConfig) (string, error) {
	target := strings.TrimSpace(cfg.Policy.Target)
	if target == "" {
		return "", fmt.Errorf("sensor de porta TCP sem alvo")
	}
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target, nil
	}
	port := cfg.Options.Int(0, "port", "porta")
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("porta TCP invalida no alvo %q (use host:porta ou optionsJson com port)", target)
	}
	return net.JoinHostPort(strings.Trim(target, "[]"), strconv.Itoa(port)), nil
}

func collectTCPPortSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	address, err := sensorTCPAddress(cfg)
	if err != nil {
		return sensorCollection{Err: err.Error()}
	}
	timeout := cfg.Timeout
	if timeout <= 0 || timeout > sensorMaxTimeout {
		timeout = sensorMaxTimeout
	}
	dialer := net.Dialer{Timeout: timeout}
	started := time.Now()
	conn, dialErr := dialer.DialContext(ctx, "tcp", address)
	elapsedMs := float64(time.Since(started).Microseconds()) / 1000

	if dialErr != nil {
		if ctx.Err() != nil {
			return sensorCollection{Measure: SensorMeasure{TimedOut: true, Running: sensorBool(false)}, Err: "tempo esgotado ao conectar"}
		}
		// Porta fechada é EVIDÊNCIA (o servidor decide Failing), não erro.
		return sensorCollection{Measure: SensorMeasure{Running: sensorBool(false)}}
	}
	_ = conn.Close()
	return sensorCollection{Measure: SensorMeasure{
		Running:   sensorBool(true),
		ConnectMs: sensorFloat(math.Round(elapsedMs*100) / 100),
	}}
}

// ── HTTP probe ──

func collectHTTPProbeSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	target := strings.TrimSpace(cfg.Policy.Target)
	if target == "" {
		return sensorCollection{Err: "sensor HTTP sem alvo (URL)"}
	}
	endpoint := target
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	method := strings.ToUpper(cfg.Options.String("method", "verb"))
	if method == "" {
		method = http.MethodGet
	}

	var (
		dnsStart, dnsDone         time.Time
		connectStart, connectDone time.Time
		tlsStart, tlsDone         time.Time
		firstByte                 time.Time
	)
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { dnsDone = time.Now() },
		ConnectStart:         func(string, string) { connectStart = time.Now() },
		ConnectDone:          func(string, string, error) { connectDone = time.Now() },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { tlsDone = time.Now() },
		GotFirstResponseByte: func() { firstByte = time.Now() },
	}

	timeout := cfg.Timeout
	if timeout <= 0 || timeout > sensorMaxTimeout {
		timeout = sensorMaxTimeout
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout}

	requestCtx, cancel := context.WithTimeout(httptrace.WithClientTrace(ctx, trace), timeout)
	defer cancel()

	started := time.Now()
	request, err := http.NewRequestWithContext(requestCtx, method, endpoint, nil)
	if err != nil {
		return sensorCollection{Err: fmt.Sprintf("URL invalida para o sensor HTTP: %v", err)}
	}
	request.Header.Set("User-Agent", "DiscoveryAgent-Sensor/1.0")

	response, err := client.Do(request)
	totalMs := float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		if requestCtx.Err() != nil || ctx.Err() != nil {
			return sensorCollection{Measure: SensorMeasure{TimedOut: true}, Err: "tempo esgotado na sonda HTTP"}
		}
		return sensorCollection{Err: fmt.Sprintf("falha na sonda HTTP: %v", err)}
	}
	defer response.Body.Close()

	written, _ := io.Copy(io.Discard, response.Body)
	if written == 0 && response.ContentLength > 0 {
		written = response.ContentLength
	}

	measure := SensorMeasure{
		StatusCode: sensorInt(response.StatusCode),
		TotalMs:    sensorFloat(math.Round(totalMs*100) / 100),
	}
	if written > 0 {
		measure.Bytes = sensorFloat(float64(written))
	}
	if !dnsStart.IsZero() && !dnsDone.IsZero() {
		measure.DNSMs = sensorFloat(roundSensorMsDuration(dnsDone.Sub(dnsStart)))
	}
	if !connectStart.IsZero() && !connectDone.IsZero() {
		measure.ConnectMs = sensorFloat(roundSensorMsDuration(connectDone.Sub(connectStart)))
	}
	if !tlsStart.IsZero() && !tlsDone.IsZero() {
		measure.TLSMs = sensorFloat(roundSensorMsDuration(tlsDone.Sub(tlsStart)))
	}
	if !firstByte.IsZero() {
		measure.TTFBMs = sensorFloat(roundSensorMsDuration(firstByte.Sub(started)))
	}
	return sensorCollection{Measure: measure}
}

func roundSensorMsDuration(value time.Duration) float64 {
	return math.Round(float64(value)/float64(time.Millisecond)*100) / 100
}

// ── DNS latency ──

// sensorDNSLookupFunc resolve um hostname. É um PARÂMETRO (e não uma chamada
// direta ao net.DefaultResolver) para os testes cobrirem o caminho de erro sem
// depender de rede/DNS externo — e continuarem determinísticos no CI.
type sensorDNSLookupFunc func(ctx context.Context, host string) ([]string, error)

// collectDNSLatencySample mede o tempo de resolução do alvo. Uma amostra só,
// sem targetKey: diferentemente do disco, DNS não tem "todos os alvos".
func collectDNSLatencySample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	return collectDNSLatencySampleWith(ctx, cfg, net.DefaultResolver.LookupHost)
}

// collectDNSLatencySampleWith faz N resoluções e devolve a MÉDIA em ResolveMs.
// Erro de resolução é erro de COLETA (servidor marca Unknown + RunError): um
// "0 ms" inventado esconderia um DNS quebrado atrás de um PASSING.
func collectDNSLatencySampleWith(ctx context.Context, cfg *SensorConfig, lookup sensorDNSLookupFunc) sensorCollection {
	host, err := sensorDNSHost(cfg.Policy.Target)
	if err != nil {
		return sensorCollection{Err: err.Error()}
	}
	samples := cfg.Options.Int(3, "samples", "count", "queries")
	if samples <= 0 {
		samples = 3
	}
	if samples > 10 {
		// Teto: DNS é rápido, e um link/perfil DNS lento não pode segurar o
		// semáforo de coleta por dezenas de resoluções.
		samples = 10
	}
	timeout := cfg.Timeout
	if timeout <= 0 || timeout > sensorMaxTimeout {
		timeout = sensorMaxTimeout
	}
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	total := time.Duration(0)
	completed := 0
	for index := 0; index < samples; index++ {
		if lookupCtx.Err() != nil {
			break
		}
		started := time.Now()
		_, lookupErr := lookup(lookupCtx, host)
		elapsed := time.Since(started)
		if lookupErr != nil {
			if lookupCtx.Err() != nil || ctx.Err() != nil {
				return sensorCollection{Measure: SensorMeasure{TimedOut: true}, Err: fmt.Sprintf("tempo esgotado ao resolver %q", host)}
			}
			return sensorCollection{Err: fmt.Sprintf("falha ao resolver %q: %v", host, lookupErr)}
		}
		total += elapsed
		completed++
	}
	if completed == 0 {
		return sensorCollection{Measure: SensorMeasure{TimedOut: true}, Err: fmt.Sprintf("tempo esgotado ao resolver %q", host)}
	}
	averageMs := float64(total) / float64(completed) / float64(time.Millisecond)
	// 3 casas: resolução via cache/hosts é sub-milissegundo e 2 casas
	// publicariam "0 ms" (evidência válida virando "sem dado").
	return sensorCollection{Measure: SensorMeasure{ResolveMs: sensorFloat(math.Round(averageMs*1000) / 1000)}}
}

// sensorDNSHost normaliza o alvo. "host", "host:53" e "[::1]:53" resolvem o
// MESMO hostname: a porta é aceita porque o editor do console compartilha o
// formato do TcpPort, mas a resolução é sempre do host.
func sensorDNSHost(raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", fmt.Errorf("sensor de DNS sem alvo (hostname)")
	}
	if host, port, err := net.SplitHostPort(target); err == nil && host != "" && port != "" {
		if _, convErr := strconv.Atoi(port); convErr == nil {
			return host, nil
		}
	}
	return strings.Trim(target, "[]"), nil
}

// ── HTTP throughput (download) ──

const (
	// sensorDefaultThroughputBytes é o default do contrato (10 MiB).
	sensorDefaultThroughputBytes = 10 * 1024 * 1024
	// sensorMaxThroughputBytes limita o corpo baixado (1 GiB): configuração
	// errada não pode consumir a banda do parque inteiro.
	sensorMaxThroughputBytes = 1024 * 1024 * 1024
	// sensorDefaultThroughputDurationMs é o teto padrão do download (8s).
	sensorDefaultThroughputDurationMs = 8000
)

// collectHTTPThroughputSample baixa um URL e mede a velocidade de download.
// Upload NÃO é coletado nesta versão: o contrato tem UploadMbps, mas o servidor
// deriva apenas os canais presentes na evidência.
func collectHTTPThroughputSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	bytesTarget := int64(cfg.Options.Int(sensorDefaultThroughputBytes, "bytes", "downloadBytes", "size", "payloadBytes"))
	if bytesTarget <= 0 {
		bytesTarget = sensorDefaultThroughputBytes
	}
	if bytesTarget > sensorMaxThroughputBytes {
		bytesTarget = sensorMaxThroughputBytes
	}
	durationMs := cfg.Options.Int(sensorDefaultThroughputDurationMs, "durationMs", "duration", "durationMillis", "maxDurationMs")
	if durationMs <= 0 {
		durationMs = sensorDefaultThroughputDurationMs
	}
	if durationMs > int(sensorMaxTimeout/time.Millisecond) {
		durationMs = int(sensorMaxTimeout / time.Millisecond)
	}

	endpoint := strings.TrimSpace(cfg.Options.String("url", "endpoint", "downloadUrl"))
	if endpoint == "" {
		// Endpoint público de speed test: devolve exatamente <bytes> bytes, o
		// que permite medir OOKLA-like sem depender de infraestrutura nossa.
		endpoint = fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d", bytesTarget)
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}

	timeout := cfg.Timeout
	if timeout <= 0 || timeout > sensorMaxTimeout {
		timeout = sensorMaxTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		ForceAttemptHTTP2: true,
		// Sem descompressão automática: o throughput é dos BYTES que trafegaram;
		// gzip inflaria o Mbps artificialmente (e o servidor compara com limiar).
		DisableCompression: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout}

	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return sensorCollection{Err: fmt.Sprintf("URL invalida para o sensor de throughput: %v", err)}
	}
	request.Header.Set("User-Agent", "DiscoveryAgent-Sensor/1.0")
	request.Header.Set("Accept-Encoding", "identity")

	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		if sensorHTTPTimedOut(err, requestCtx) || ctx.Err() != nil {
			return sensorCollection{Measure: SensorMeasure{TimedOut: true}, Err: "tempo esgotado ao baixar do URL de throughput"}
		}
		return sensorCollection{Err: fmt.Sprintf("falha ao baixar do URL de throughput: %v", err)}
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return sensorCollection{Measure: SensorMeasure{StatusCode: sensorInt(response.StatusCode)}, Err: fmt.Sprintf("URL de throughput respondeu HTTP %d", response.StatusCode)}
	}

	// Para no PRIMEIRO limite: <bytes> (tamanho do teste) ou durationMs (link
	// lento não pode segurar o semáforo de coleta até o timeout do sensor).
	deadline := started.Add(time.Duration(durationMs) * time.Millisecond)
	reader := io.LimitReader(response.Body, bytesTarget)
	buffer := make([]byte, 32*1024)
	var downloaded int64
	for downloaded < bytesTarget {
		if !time.Now().Before(deadline) {
			break
		}
		read, readErr := reader.Read(buffer)
		downloaded += int64(read)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if sensorHTTPTimedOut(readErr, requestCtx) || ctx.Err() != nil {
				return sensorCollection{Measure: SensorMeasure{TimedOut: true}, Err: "tempo esgotado ao baixar do URL de throughput"}
			}
			return sensorCollection{Err: fmt.Sprintf("falha no download de throughput: %v", readErr)}
		}
	}

	elapsed := time.Since(started)
	if downloaded <= 0 {
		return sensorCollection{Err: "nenhum byte recebido do URL de throughput"}
	}
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		// Relógio com resolução grosseira: evita divisão por zero e +Inf (não é
		// JSON válido na cláusula de sanidade do canal).
		seconds = 1e-9
	}
	downloadMbps := float64(downloaded) * 8 / 1_000_000 / seconds
	return sensorCollection{Measure: SensorMeasure{
		StatusCode:   sensorInt(response.StatusCode),
		TotalMs:      sensorFloat(math.Round(float64(elapsed)/float64(time.Millisecond)*100) / 100),
		Bytes:        sensorFloat(float64(downloaded)),
		DownloadMbps: sensorFloat(math.Round(downloadMbps*1000) / 1000),
	}}
}

// sensorHTTPTimedOut classifica o erro como TIMEOUT mesmo quando quem venceu a
// corrida foi o timeout interno do http.Client (que cancela o contexto DERIVADO
// da request, e não o contexto que passamos) — sem isso um link lento viraria
// "falha" genérica em vez de TimedOut.
func sensorHTTPTimedOut(err error, ctx context.Context) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeoutErr net.Error
	return errors.As(err, &timeoutErr) && timeoutErr.Timeout()
}

// ── Utilidades ──

// runHiddenCommand executa um utilitário do Windows sem janela e devolve
// stdout+stderr. Exit code != 0 NÃO é erro fatal: o chamador parseia a saída
// (ping sai com 1 quando perde pacotes, sc sai com 1060 quando não existe).
func runHiddenCommand(ctx context.Context, timeout time.Duration, executable string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, executable, args...)
	processutil.HideWindow(command)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	combined := strings.TrimSpace(stdout.String())
	if stderrText := strings.TrimSpace(stderr.String()); stderrText != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += stderrText
	}
	return combined, err
}
