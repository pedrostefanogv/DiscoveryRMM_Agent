//go:build windows

package automation

// ─────────────────────────────────────────────────────────────────────────────
// Coletores NATIVOS da FASE 5 de sensores (Windows).
//
// Tipos cobertos aqui: Bandwidth, DiskIo, EventLogRate, SmartStatus,
// CertificateExpiry, DefenderStatus e BitLocker — mais o restartIfStopped do
// sensor de Serviço (o coletor de SCM continua em sensors_collect_windows.go).
//
// REGRA DE OURO do recurso: o agente MEDE, o servidor DECIDE. Nenhum limiar é
// avaliado aqui; erro de COLETA vira sensorCollection.Err (servidor marca
// Unknown + RunError) e "sem dado" é nil — nunca um número inventado.
//
// Limitações de cada coletor estão documentadas no bloco do próprio coletor.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"

	"discovery/app/core/inventory"
)

// ── Defaults / tetos ──

const (
	// sensorDefaultSampleWindowMs é a janela padrão de uma amostra diferencial
	// (Bandwidth e DiskIo): curta para não segurar o semáforo de coleta.
	sensorDefaultSampleWindowMs = 1000
	// sensorMinSampleWindowMs evita delta dividido por uma janela degenerada.
	sensorMinSampleWindowMs = 100
	// sensorMaxSampleWindowMs é o teto mesmo quando options.sampleMs pede mais.
	sensorMaxSampleWindowMs = 10000
	// sensorDefaultEventLogWindowMinutes espelha o default documentado.
	sensorDefaultEventLogWindowMinutes = 60
	// sensorMaxEventLogEvents limita a enumeração do log (evidência, não relatório).
	sensorMaxEventLogEvents = 5000
	// sensorDefaultCertificatePort é a porta TLS usada quando o alvo é "host".
	sensorDefaultCertificatePort = 443
	// sensorPowershellTimeout é o orçamento máximo de um subprocesso PowerShell
	// destes coletores (o timeout do sensor continua sendo o teto real).
	sensorPowershellTimeout = 45 * time.Second
	// sensorDefaultRestartWaitMs é a espera padrão do restartIfStopped.
	sensorDefaultRestartWaitMs = 5000
	// sensorMaxRestartWaitMs é o teto da espera (30s): o motor roda a cada ~15s.
	sensorMaxRestartWaitMs = 30000
)

// ── Utilidades comuns ──

// sensorCommandTimeout escolhe o timeout do subprocesso: o menor entre o
// timeout do sensor e o teto do coletor.
func sensorCommandTimeout(cfg *SensorConfig, ceiling time.Duration) time.Duration {
	if cfg != nil && cfg.Timeout > 0 && cfg.Timeout < ceiling {
		return cfg.Timeout
	}
	return ceiling
}

// sensorSampleWindowMs normaliza options.sampleMs (default 1000, 100..10000).
func sensorSampleWindowMs(cfg *SensorConfig) int {
	window := cfg.Options.Int(sensorDefaultSampleWindowMs, "sampleMs", "sample", "windowMs", "intervalMs")
	if window < sensorMinSampleWindowMs {
		window = sensorMinSampleWindowMs
	}
	if window > sensorMaxSampleWindowMs {
		window = sensorMaxSampleWindowMs
	}
	return window
}

// sensorWaitContext espera a janela respeitando o cancelamento da coleta.
func sensorWaitContext(ctx context.Context, milliseconds int) bool {
	if milliseconds <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(time.Duration(milliseconds) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// runSensorPowerShell executa um script PowerShell sem janela e devolve a saída
// combinada. Mesmo padrão do sensor de script (sem ApplyUserContext): roda como a
// identidade do agente — no modo serviço, SYSTEM.
func runSensorPowerShell(ctx context.Context, timeout time.Duration, script string) (string, error) {
	return runHiddenCommand(ctx, timeout, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-WindowStyle", "Hidden", "-Command", script)
}

// decodeSensorJSON decodifica o PRIMEIRO objeto/array JSON da saída de um
// utilitário. Saídas reais misturam avisos/linhas em branco antes do JSON.
func decodeSensorJSON(text string, dest any) error {
	trimmed := strings.TrimSpace(text)
	for index := 0; index < len(trimmed); index++ {
		if trimmed[index] != '{' && trimmed[index] != '[' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(trimmed[index:]))
		if err := decoder.Decode(dest); err == nil {
			return nil
		}
	}
	return fmt.Errorf("nenhum JSON valido na saida do comando")
}

// sensorRound2 arredonda para 2 casas decimais.
func sensorRound2(value float64) float64 { return math.Round(value*100) / 100 }

// ─────────────────────────────────────────────────────────────────────────────
// 1) BANDWIDTH — rx_bps/tx_bps somando adaptadores ativos
//
// Fonte: iphlpapi!GetIfTable (MIB_IFTABLE/MIB_IFROW, API estável). Amostramos os
// contadores de TODOS os adaptadores elegíveis, esperamos options.sampleMs
// (default 1000ms) e amostramos de novo; o delta de octetos dividido pela janela
// é a taxa.
//
// Elegível = operacional/conectado, não loopback (IF_TYPE_SOFTWARE_LOOPBACK=24)
// e não túnel (IF_TYPE_TUNNEL=131). Adaptadores derrubados ficam de fora para o
// número refletir tráfego REAL (o "cabo desconectado" do Windows continua
// reportando contadores antigos).
//
// Limitações:
//   - MIB_IFROW expõe contadores de 32 bits (dwInOctets/dwOutOctets): a diferença
//     é feita em aritmética MODULAR uint32, exata para janelas curtas (1–10 s);
//     não serve para janelas de horas.
//   - Um adaptador que aparece/some entre as duas amostras é ignorado (sem delta
//     válido) — evita picos falsos ao plugar/desplugar VPN.
//   - Somamos TODAS as interfaces ativas (host inteiro): não há "por adaptador"
//     no contrato e um sensor por adaptador explodiria a cardinalidade.
// ─────────────────────────────────────────────────────────────────────────────

const (
	ifTypeSoftwareLoopback  = 24
	ifTypeTunnel            = 131
	ifOperStatusConnected   = 4
	ifOperStatusOperational = 5
	// sensorMibIfRowSize é sizeof(MIB_IFROW) no Windows x86/x64.
	sensorMibIfRowSize = 860
)

var (
	modiphlpapiSensors    = syscall.NewLazyDLL("iphlpapi.dll")
	procGetIfTableSensors = modiphlpapiSensors.NewProc("GetIfTable")
)

// sensorIfRow é o subconjunto de MIB_IFROW que o coletor usa.
type sensorIfRow struct {
	Name       string
	Index      uint32
	Type       uint32
	OperStatus uint32
	InOctets   uint32
	OutOctets  uint32
}

// sensorInterfaceCounters é o par de contadores (bytes) de UMA interface.
type sensorInterfaceCounters struct {
	rx uint64
	tx uint64
}

// decodeSensorUTF16 decodifica o WCHAR[] do MIB_IFROW até o terminador NUL.
func decodeSensorUTF16(buffer []byte) string {
	if len(buffer) < 2 {
		return ""
	}
	length := len(buffer) / 2
	units := make([]uint16, 0, length)
	for index := 0; index < length; index++ {
		unit := binary.LittleEndian.Uint16(buffer[index*2 : index*2+2])
		if unit == 0 {
			break
		}
		units = append(units, unit)
	}
	return strings.TrimSpace(syscall.UTF16ToString(units))
}

// parseSensorIfTable decodifica o buffer cru de GetIfTable (DWORD dwNumEntries
// seguido de dwNumEntries MIB_IFROW). Função PURA: os testes montam o buffer.
func parseSensorIfTable(buffer []byte) []sensorIfRow {
	if len(buffer) < 4 {
		return nil
	}
	count := int(binary.LittleEndian.Uint32(buffer[:4]))
	if count <= 0 || count > 4096 {
		return nil
	}
	rows := make([]sensorIfRow, 0, count)
	for index := 0; index < count; index++ {
		offset := 4 + index*sensorMibIfRowSize
		if offset+sensorMibIfRowSize > len(buffer) {
			break
		}
		row := buffer[offset : offset+sensorMibIfRowSize]
		rows = append(rows, sensorIfRow{
			Name:       decodeSensorUTF16(row[0:512]),
			Index:      binary.LittleEndian.Uint32(row[512:516]),
			Type:       binary.LittleEndian.Uint32(row[516:520]),
			OperStatus: binary.LittleEndian.Uint32(row[544:548]),
			InOctets:   binary.LittleEndian.Uint32(row[552:556]),
			OutOctets:  binary.LittleEndian.Uint32(row[576:580]),
		})
	}
	return rows
}

// sensorIfRowEligible informa se a interface entra na soma de banda.
func sensorIfRowEligible(row sensorIfRow) bool {
	if row.Type == ifTypeSoftwareLoopback || row.Type == ifTypeTunnel {
		return false
	}
	return row.OperStatus == ifOperStatusConnected || row.OperStatus == ifOperStatusOperational
}

// readInterfaceCounters lê GetIfTable e devolve os contadores das interfaces
// elegíveis, indexados pelo número da interface (IfIndex).
func readInterfaceCounters() map[string]sensorInterfaceCounters {
	var size uint32
	if ret, _, _ := procGetIfTableSensors.Call(0, uintptr(unsafe.Pointer(&size)), 1); size == 0 {
		_ = ret
		return nil
	}
	buffer := make([]byte, size)
	ret, _, _ := procGetIfTableSensors.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)), 1)
	if ret != 0 {
		return nil
	}
	used := int(size)
	if used > len(buffer) {
		used = len(buffer)
	}
	counters := make(map[string]sensorInterfaceCounters, 8)
	for _, row := range parseSensorIfTable(buffer[:used]) {
		if !sensorIfRowEligible(row) {
			continue
		}
		counters[strconv.FormatUint(uint64(row.Index), 10)] = sensorInterfaceCounters{
			rx: uint64(row.InOctets),
			tx: uint64(row.OutOctets),
		}
	}
	return counters
}

// sensorCounterDeltaBps calcula (bytes/segundo) de um contador acumulado.
// Contador que regrediu (reset do adaptador) vira 0 em vez de pico negativo.
func sensorCounterDeltaBps(previous, current uint64, seconds float64) float64 {
	if seconds <= 0 || current < previous {
		return 0
	}
	return float64(current-previous) / seconds
}

// collectBandwidthSample mede rx/tx do host em uma janela curta.
func collectBandwidthSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	windowMs := sensorSampleWindowMs(cfg)
	started := time.Now()
	before := readInterfaceCounters()
	if len(before) == 0 {
		return sensorCollection{Err: "nenhuma interface de rede ativa para medir banda (GetIfTable sem adaptadores elegiveis)"}
	}
	if !sensorWaitContext(ctx, windowMs) {
		return sensorCollection{Measure: SensorMeasure{TimedOut: true}, Err: "tempo esgotado esperando a janela de banda"}
	}
	after := readInterfaceCounters()
	if len(after) == 0 {
		return sensorCollection{Err: "falha ao reler os contadores de interface de rede"}
	}
	// Janela REAL entre as duas leituras (não o sampleMs pedido): o tempo de
	// GetIfTable entra na conta e evita taxa ligeiramente inflada.
	seconds := time.Since(started).Seconds()
	var rx, tx float64
	for index, current := range after {
		previous, seen := before[index]
		if !seen {
			continue // interface que subiu no meio da janela: sem baseline
		}
		rx += sensorCounterDeltaBps(previous.rx, current.rx, seconds)
		tx += sensorCounterDeltaBps(previous.tx, current.tx, seconds)
	}
	return sensorCollection{Measure: SensorMeasure{
		RxBps: sensorFloat(sensorRound2(rx)),
		TxBps: sensorFloat(sensorRound2(tx)),
	}}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2) DISK IO — read_bps/write_bps/busy_percent/latency_ms do disco alvo
//
// Fonte primária: IOCTL_DISK_PERFORMANCE em \\.\PhysicalDriveN (contadores
// acumulados do driver: bytes lidos/escritos, tempos em 100ns e nº de
// transferências). Duas leituras separadas por options.sampleMs (default 1000ms)
// dão as taxas, o percentual de ocupação (100 - idle) e a latência média por
// transferência.
//
// Alvo (options.disk, default = disco do sistema):
//   - vazio            → %SystemDrive% (ex.: "C:") mapeado ao disco físico;
//   - "C:", "C"        → letra de volume mapeada via Get-Partition/Get-Disk;
//   - \\.\PhysicalDrive0, "PhysicalDrive0", "0" → número direto;
//   - qualquer outro   → FriendlyName/SerialNumber em Get-PhysicalDisk.
//
// Fallback: se o IOCTL falhar (política de disco/RAID/permissão), usa o coletor
// PDH do pacote inventory (exportado em inventory.CollectDiskIONativePercent)
// para busyPercent/latencyMs. Nesse caminho os BYTES/S não existem (o PDH
// consultado não expõe \Disk Bytes/sec) e o percentual é do conjunto _Total
// (todos os discos), não do disco alvo — limitação documentada.
// ─────────────────────────────────────────────────────────────────────────────

const sensorIOCTLDiskPerformance = 0x00070020

// sensorDiskPerformance é DISK_PERFORMANCE (winioctl.h). Layout C:
// 5 LARGE_INTEGER + 4 DWORD + LARGE_INTEGER + DWORD + WCHAR[8].
type sensorDiskPerformance struct {
	BytesRead           int64
	BytesWritten        int64
	ReadTime            int64
	WriteTime           int64
	IdleTime            int64
	ReadCount           uint32
	WriteCount          uint32
	QueueDepth          uint32
	SplitCount          uint32
	QueryTime           int64
	StorageDeviceNumber uint32
	StorageManagerName  [8]uint16
}

// sensorCounterDelta ignora regressão (contador zerado/reiniciado).
func sensorCounterDelta(current, previous int64) int64 {
	if current < previous {
		return 0
	}
	return current - previous
}

// readSensorDiskPerformance lê IOCTL_DISK_PERFORMANCE de um disco físico.
func readSensorDiskPerformance(driveNumber uint32) (sensorDiskPerformance, error) {
	var performance sensorDiskPerformance
	path := fmt.Sprintf(`\\.\PhysicalDrive%d`, driveNumber)
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return performance, fmt.Errorf("caminho do disco invalido: %w", err)
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
	handle, err := windows.CreateFile(pathPtr, windows.GENERIC_READ, share, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		// Disco pode negar GENERIC_READ; IOCTL_DISK_PERFORMANCE é FILE_ANY_ACCESS.
		handle, err = windows.CreateFile(pathPtr, 0, share, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			return performance, fmt.Errorf("nao foi possivel abrir %s: %w", path, err)
		}
	}
	defer windows.CloseHandle(handle)

	var returned uint32
	if err := windows.DeviceIoControl(
		handle,
		sensorIOCTLDiskPerformance,
		nil,
		0,
		(*byte)(unsafe.Pointer(&performance)),
		uint32(unsafe.Sizeof(performance)),
		&returned,
		nil,
	); err != nil {
		return sensorDiskPerformance{}, fmt.Errorf("IOCTL_DISK_PERFORMANCE falhou em %s: %w", path, err)
	}
	return performance, nil
}

// computeDiskIOMetrics calcula as taxas/ocupação/latência entre duas leituras.
// Função PURA: os testes passam snapshots sintéticos com janela conhecida.
func computeDiskIOMetrics(previous, current sensorDiskPerformance, elapsedSeconds float64) (readBps, writeBps, busyPercent, latencyMs float64, hasBusy, hasLatency bool) {
	if elapsedSeconds <= 0 {
		return 0, 0, 0, 0, false, false
	}
	readBps = float64(sensorCounterDelta(current.BytesRead, previous.BytesRead)) / elapsedSeconds
	writeBps = float64(sensorCounterDelta(current.BytesWritten, previous.BytesWritten)) / elapsedSeconds

	// % ocupado = 100 - % idle. Só é confiável quando o driver mantém IdleTime
	// (drivers antigos/RAID podem devolver sempre 0 → dado ausente, não 100%).
	if current.IdleTime != 0 || previous.IdleTime != 0 {
		idleSeconds := float64(sensorCounterDelta(current.IdleTime, previous.IdleTime)) / 1e7
		busy := 100 * (1 - idleSeconds/elapsedSeconds)
		if busy < 0 {
			busy = 0
		}
		if busy > 100 {
			busy = 100
		}
		busyPercent = busy
		hasBusy = true
	}

	transfers := uint64(uint32(current.ReadCount-previous.ReadCount)) + uint64(uint32(current.WriteCount-previous.WriteCount))
	if transfers > 0 {
		serviceTime := sensorCounterDelta(current.ReadTime, previous.ReadTime) + sensorCounterDelta(current.WriteTime, previous.WriteTime)
		if serviceTime > 0 {
			// Tempos em 100ns → ms por transferência.
			latencyMs = float64(serviceTime) / float64(transfers) / 10000
			hasLatency = true
		}
	}
	return readBps, writeBps, busyPercent, latencyMs, hasBusy, hasLatency
}

// parseSensorPhysicalDriveNumber reconhece os formatos diretos de alvo.
func parseSensorPhysicalDriveNumber(raw string) (uint32, bool) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return 0, false
	}
	target = strings.TrimPrefix(target, `\\.\`)
	target = strings.TrimPrefix(target, `\\?\`)
	target = strings.TrimPrefix(strings.ToLower(target), "physicaldrive")
	number, err := strconv.ParseUint(strings.TrimSpace(target), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(number), true
}

// sensorVolumeLetterPattern aceita "C:", "C" e "c:\".
var sensorVolumeLetterPattern = regexp.MustCompile(`^([A-Za-z])(?::[\\/]*)?$`)

// lastNonEmptyLine devolve a última linha não vazia da saída (PowerShell pode
// emitir avisos antes do valor).
func lastNonEmptyLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if trimmed := strings.TrimSpace(lines[index]); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// resolveSensorDiskTarget devolve (número do disco físico, targetKey, erro).
func resolveSensorDiskTarget(ctx context.Context, cfg *SensorConfig) (uint32, string, error) {
	raw := strings.TrimSpace(cfg.Options.String("disk", "drive", "device", "physicalDrive"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("SystemDrive"))
		if raw == "" {
			raw = "C:"
		}
	}
	if number, ok := parseSensorPhysicalDriveNumber(raw); ok {
		return number, fmt.Sprintf("PhysicalDrive%d", number), nil
	}
	if match := sensorVolumeLetterPattern.FindStringSubmatch(raw); match != nil {
		letter := strings.ToUpper(match[1])
		script := fmt.Sprintf(`try { $p = Get-Partition -DriveLetter '%s' -ErrorAction Stop | Select-Object -First 1; if (-not $p) { throw 'particao nao encontrada' }; [int]((Get-Disk -Number $p.DiskNumber -ErrorAction Stop).Number) } catch { throw }`, letter)
		output, err := runSensorPowerShell(ctx, sensorCommandTimeout(cfg, sensorPowershellTimeout), script)
		if err != nil {
			return 0, "", fmt.Errorf("nao foi possivel mapear o volume %s para um disco fisico: %v", raw, err)
		}
		number, convErr := strconv.ParseUint(strings.TrimSpace(lastNonEmptyLine(output)), 10, 32)
		if convErr != nil {
			return 0, "", fmt.Errorf("mapeamento do volume %s devolveu %q (esperado numero de disco)", raw, output)
		}
		return uint32(number), letter + ":", nil
	}
	if strings.ContainsAny(raw, `'";|`) {
		return 0, "", fmt.Errorf("alvo de disco invalido: %q", raw)
	}
	// Nome amigável/serial: casa com Get-PhysicalDisk.
	script := fmt.Sprintf(`$t = '%s'; $d = Get-PhysicalDisk -ErrorAction Stop | Where-Object { $_.FriendlyName -eq $t -or $_.SerialNumber -eq $t } | Select-Object -First 1; if (-not $d) { throw 'disco nao encontrado' }; [int]$d.DeviceId`, raw)
	output, err := runSensorPowerShell(ctx, sensorCommandTimeout(cfg, sensorPowershellTimeout), script)
	if err != nil {
		return 0, "", fmt.Errorf("disco %q nao encontrado: %v", raw, err)
	}
	number, convErr := strconv.ParseUint(strings.TrimSpace(lastNonEmptyLine(output)), 10, 32)
	if convErr != nil {
		return 0, "", fmt.Errorf("discos fisicos nao retornaram numero para %q: %q", raw, output)
	}
	return uint32(number), raw, nil
}

// collectDiskIOSample mede I/O do disco alvo (uma amostra, targetKey do disco).
func collectDiskIOSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	windowMs := sensorSampleWindowMs(cfg)
	driveNumber, targetKey, err := resolveSensorDiskTarget(ctx, cfg)
	if err != nil {
		return sensorCollection{Err: err.Error()}
	}
	started := time.Now()
	previous, err := readSensorDiskPerformance(driveNumber)
	if err != nil {
		return collectDiskIOFallbackPDH(ctx, windowMs, targetKey, err)
	}
	if !sensorWaitContext(ctx, windowMs) {
		return sensorCollection{TargetKey: targetKey, Measure: SensorMeasure{TimedOut: true}, Err: "tempo esgotado esperando a janela de I/O de disco"}
	}
	current, err := readSensorDiskPerformance(driveNumber)
	if err != nil {
		return collectDiskIOFallbackPDH(ctx, windowMs, targetKey, err)
	}
	elapsed := time.Since(started).Seconds()
	readBps, writeBps, busyPercent, latencyMs, hasBusy, hasLatency := computeDiskIOMetrics(previous, current, elapsed)

	measure := SensorMeasure{
		ReadBps:  sensorFloat(sensorRound2(readBps)),
		WriteBps: sensorFloat(sensorRound2(writeBps)),
	}
	if hasBusy {
		measure.BusyPercent = sensorFloat(roundSensorPercent(busyPercent))
	}
	if hasLatency {
		measure.DiskLatencyMs = sensorFloat(sensorRound2(latencyMs))
	}
	return sensorCollection{TargetKey: targetKey, Measure: measure}
}

// collectDiskIOFallbackPDH cobre o caso em que IOCTL_DISK_PERFORMANCE é negado.
func collectDiskIOFallbackPDH(ctx context.Context, windowMs int, targetKey string, cause error) sensorCollection {
	// A primeira chamada do PDH só semeia a baseline; a leitura boa vem depois.
	_, _, _, _, _ = inventory.CollectDiskIONativePercent()
	if !sensorWaitContext(ctx, windowMs) {
		return sensorCollection{TargetKey: targetKey, Measure: SensorMeasure{TimedOut: true}, Err: "tempo esgotado esperando a janela de I/O de disco (fallback PDH)"}
	}
	busyPercent, _, _, latencyMs, ok := inventory.CollectDiskIONativePercent()
	if !ok {
		return sensorCollection{TargetKey: targetKey, Err: fmt.Sprintf("falha ao medir I/O do disco: %v", cause)}
	}
	measure := SensorMeasure{BusyPercent: sensorFloat(roundSensorPercent(busyPercent))}
	if latencyMs >= 0 {
		measure.DiskLatencyMs = sensorFloat(sensorRound2(latencyMs))
	}
	return sensorCollection{TargetKey: targetKey, Measure: measure}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3) SMART — smartStatus + temperatureC/powerOnHours/reallocatedSectors
//
// Fonte: WMI/CIM, o caminho disponível para o agente sem exigir driver do
// fabricante:
//   - MSFT_PhysicalDisk (Get-PhysicalDisk): HealthStatus (Healthy|Warning|
//     Unhealthy|Unknown) e DeviceId (número do disco físico);
//   - MSFT_StorageReliabilityCounter (Get-StorageReliabilityCounter):
//     Temperature e PowerOnHours;
//   - MSStorageDriver_FailurePredictData (root\wmi): dados SMART crus (512
//     bytes por disco) — é daqui que vem ReallocatedSectors (atributo 5) e os
//     fallbacks de temperatura (194/190) e horas ligadas (9).
//
// Limitação: MSStorageDriver_FailurePredictData só existe para discos ATA/SATA
// atrás de controladora que repassa o SMART; NVMe/RAID podem não expor nada
// (nesse caso os campos ficam nil — "sem dado", nunca zero inventado). O IOCTL
// SMART_RCV_DRIVE_DATA não é usado porque depende de structs
// SENDCMDINPARAMS/ATA pass-through que falham justamente nos controladores
// acima; o WMI cobre o mesmo dado com menos superfície.
//
// options.disk vazio = TODOS os discos físicos e o PIOR estado vence. Para as
// medidas agregadas usamos o extremo conservador: pior SmartStatus, maior
// temperatura, mais horas ligadas e mais setores realocados.
// ─────────────────────────────────────────────────────────────────────────────

// sensorPhysicalDiskInfo é um item do Get-PhysicalDisk.
type sensorPhysicalDiskInfo struct {
	DeviceID     string   `json:"deviceId"`
	Name         string   `json:"name"`
	Health       string   `json:"health"`
	Temperature  *float64 `json:"temperature"`
	PowerOnHours *float64 `json:"powerOnHours"`
}

// sensorSmartVendorData é uma instância de MSStorageDriver_FailurePredictData
// com os 512 bytes em base64 (JSON não transporta byte[] de forma portátil).
type sensorSmartVendorData struct {
	Instance string `json:"instance"`
	Data     string `json:"data"`
}

// sensorSmartReport é o JSON devolvido pelo script de SMART.
type sensorSmartReport struct {
	Disks []sensorPhysicalDiskInfo `json:"disks"`
	Smart []sensorSmartVendorData  `json:"smart"`
}

// sensorSmartScript devolve o JSON de discos + SMART cru. As opções do alvo são
// aplicadas no Go, então nenhum texto do usuário é interpolado no script.
const sensorSmartScript = `$ErrorActionPreference = 'SilentlyContinue'
$out = @()
foreach ($d in @(Get-PhysicalDisk)) {
  $temp = $null; $hours = $null
  try {
    $c = $d | Get-StorageReliabilityCounter
    if ($c) { $temp = $c.Temperature; $hours = $c.PowerOnHours }
  } catch { }
  $out += [pscustomobject]@{ deviceId = "$($d.DeviceId)"; name = "$($d.FriendlyName)"; health = "$($d.HealthStatus)"; temperature = $temp; powerOnHours = $hours }
}
$sm = @()
try {
  foreach ($x in @(Get-CimInstance -Namespace 'rootwmi' -ClassName MSStorageDriver_FailurePredictData -ErrorAction Stop)) {
    $sm += [pscustomobject]@{ instance = "$($x.InstanceName)"; data = [Convert]::ToBase64String($x.VendorSpecific) }
  }
} catch { }
[pscustomobject]@{ disks = $out; smart = $sm } | ConvertTo-Json -Depth 6 -Compress
`

// sensorSmartStatusRank ordena a severidade (maior = pior).
func sensorSmartStatusRank(status string) int {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "healthy":
		return 0
	case "unknown":
		return 1
	case "warning":
		return 2
	case "unhealthy", "failed":
		return 3
	default:
		return 1
	}
}

// normalizeSmartStatus converte HealthStatus em um valor do contrato
// (Healthy|Warning|Unhealthy|Unknown).
func normalizeSmartStatus(raw string) string {
	switch sensorSmartStatusRank(raw) {
	case 0:
		return "Healthy"
	case 2:
		return "Warning"
	case 3:
		return "Unhealthy"
	default:
		return "Unknown"
	}
}

// worstSmartStatus devolve o status mais grave do conjunto (Unknown se vazio).
func worstSmartStatus(disks []sensorPhysicalDiskInfo) string {
	if len(disks) == 0 {
		return "Unknown"
	}
	worst := "Healthy"
	worstRank := -1
	for _, disk := range disks {
		status := normalizeSmartStatus(disk.Health)
		if rank := sensorSmartStatusRank(status); rank > worstRank {
			worstRank = rank
			worst = status
		}
	}
	return worst
}

// selectSmartDisks filtra Get-PhysicalDisk pelo alvo (vazio = todos).
func selectSmartDisks(disks []sensorPhysicalDiskInfo, target string) []sensorPhysicalDiskInfo {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return disks
	}
	number, hasNumber := parseSensorPhysicalDriveNumber(trimmed)
	selected := make([]sensorPhysicalDiskInfo, 0, 1)
	for _, disk := range disks {
		if hasNumber {
			if strings.TrimSpace(disk.DeviceID) == strconv.FormatUint(uint64(number), 10) {
				selected = append(selected, disk)
			}
			continue
		}
		if strings.EqualFold(strings.TrimSpace(disk.DeviceID), trimmed) ||
			strings.Contains(strings.ToLower(disk.Name), strings.ToLower(trimmed)) {
			selected = append(selected, disk)
		}
	}
	return selected
}

// sensorSmartDiskNumber extrai o índice do disco do InstanceName do WMI
// ("IDEDiskST..._0", "SCSI...").
func sensorSmartDiskNumber(instance string) (uint32, bool) {
	trimmed := strings.TrimSpace(instance)
	if trimmed == "" {
		return 0, false
	}
	position := strings.LastIndex(trimmed, "_")
	if position < 0 || position == len(trimmed)-1 {
		return 0, false
	}
	number, err := strconv.ParseUint(trimmed[position+1:], 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(number), true
}

// parseSmartAttributes lê os atributos SMART de um bloco de 512 bytes
// (SMART READ DATA: 2 bytes de revisão + 30 atributos de 12 bytes).
// Devolve (setores realocados, temperatura °C, horas ligadas, ok).
func parseSmartAttributes(data []byte) (reallocated *int, temperature *float64, powerOnHours *int, ok bool) {
	if len(data) < 362 {
		return nil, nil, nil, false
	}
	parsedAny := false
	for index := 0; index < 30; index++ {
		offset := 2 + index*12
		if offset+12 > len(data) {
			break
		}
		attribute := data[offset : offset+12]
		id := attribute[0]
		if id == 0 {
			continue
		}
		raw := attribute[5:11]
		switch id {
		case 5: // Reallocated Sector Count
			reallocated = sensorInt(int(raw[0]) | int(raw[1])<<8 | int(raw[2])<<16 | int(raw[3])<<24)
			parsedAny = true
		case 9: // Power-On Hours
			powerOnHours = sensorInt(int(raw[0]) | int(raw[1])<<8 | int(raw[2])<<16 | int(raw[3])<<24)
			parsedAny = true
		case 194, 190: // Temperature / Temperature Celsius
			// 194 usa raw[0] como atual; 190 pode usar raw[0]=min, raw[2]=atual.
			// Reportamos o maior valor plausível (pior caso).
			best := -1
			for _, candidate := range []int{int(raw[0]), int(raw[2])} {
				if candidate > 0 && candidate < 130 && candidate > best {
					best = candidate
				}
			}
			if best > 0 {
				temperature = sensorFloat(float64(best))
				parsedAny = true
			}
		}
	}
	return reallocated, temperature, powerOnHours, parsedAny
}

// parseSmartReport decodifica o JSON do script e o SMART em base64 por disco.
func parseSmartReport(output string) (sensorSmartReport, map[uint32]sensorSmartVendorData, error) {
	var report sensorSmartReport
	if err := decodeSensorJSON(output, &report); err != nil {
		return report, nil, err
	}
	byDisk := make(map[uint32]sensorSmartVendorData, len(report.Smart))
	for _, item := range report.Smart {
		number, ok := sensorSmartDiskNumber(item.Instance)
		if !ok {
			continue
		}
		byDisk[number] = item
	}
	return report, byDisk, nil
}

// buildSmartStatusCollection monta a evidência agregada dos discos selecionados.
func buildSmartStatusCollection(disks []sensorPhysicalDiskInfo, smartByDisk map[uint32]sensorSmartVendorData, target string) sensorCollection {
	selected := selectSmartDisks(disks, target)
	if len(selected) == 0 {
		return sensorCollection{Err: fmt.Sprintf("nenhum disco fisico corresponde ao alvo %q", strings.TrimSpace(target))}
	}
	targetKey := ""
	if len(selected) == 1 && strings.TrimSpace(target) != "" {
		targetKey = strings.TrimSpace(selected[0].Name)
		if targetKey == "" {
			targetKey = fmt.Sprintf("PhysicalDrive%s", strings.TrimSpace(selected[0].DeviceID))
		}
	}

	measure := SensorMeasure{SmartStatus: sensorString(worstSmartStatus(selected))}
	var (
		maxTemperature  float64
		maxPowerOnHours int
		maxReallocated  int
		hasTemperature  bool
		hasPowerOnHours bool
		hasReallocated  bool
	)
	for _, disk := range selected {
		if disk.Temperature != nil && (!hasTemperature || *disk.Temperature > maxTemperature) {
			maxTemperature = *disk.Temperature
			hasTemperature = true
		}
		if disk.PowerOnHours != nil {
			hours := int(*disk.PowerOnHours)
			if !hasPowerOnHours || hours > maxPowerOnHours {
				maxPowerOnHours = hours
				hasPowerOnHours = true
			}
		}
		number, err := strconv.ParseUint(strings.TrimSpace(disk.DeviceID), 10, 32)
		if err != nil {
			continue
		}
		vendor, found := smartByDisk[uint32(number)]
		if !found {
			continue
		}
		raw, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(vendor.Data))
		if decodeErr != nil {
			continue
		}
		reallocated, temperature, powerOnHours, _ := parseSmartAttributes(raw)
		if reallocated != nil && (!hasReallocated || *reallocated > maxReallocated) {
			maxReallocated = *reallocated
			hasReallocated = true
		}
		if temperature != nil && !hasTemperature {
			maxTemperature = *temperature
			hasTemperature = true
		}
		if powerOnHours != nil && !hasPowerOnHours {
			maxPowerOnHours = *powerOnHours
			hasPowerOnHours = true
		}
	}
	if hasTemperature {
		measure.TemperatureC = sensorFloat(sensorRound2(maxTemperature))
	}
	if hasPowerOnHours {
		measure.PowerOnHours = sensorInt(maxPowerOnHours)
	}
	if hasReallocated {
		measure.ReallocatedSectors = sensorInt(maxReallocated)
	}
	return sensorCollection{TargetKey: targetKey, Measure: measure}
}

// collectSmartStatusSamples lê saúde/SMART dos discos físicos. Devolve UMA
// amostra (agregado "pior vence" quando não há alvo), conforme o contrato.
func collectSmartStatusSamples(ctx context.Context, cfg *SensorConfig) []sensorCollection {
	target := strings.TrimSpace(cfg.Options.String("disk", "drive", "device", "physicalDrive"))
	output, err := runSensorPowerShell(ctx, sensorCommandTimeout(cfg, sensorPowershellTimeout), sensorSmartScript)
	if err != nil && strings.TrimSpace(output) == "" {
		return []sensorCollection{{Err: fmt.Sprintf("falha ao consultar o SMART via WMI: %v", err)}}
	}
	report, smartByDisk, parseErr := parseSmartReport(output)
	if parseErr != nil {
		return []sensorCollection{{Err: fmt.Sprintf("saida do SMART invalida: %v", parseErr)}}
	}
	if len(report.Disks) == 0 {
		return []sensorCollection{{Err: "nenhum disco fisico reportado pelo WMI"}}
	}
	return []sensorCollection{buildSmartStatusCollection(report.Disks, smartByDisk, target)}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4) BITLOCKER — bitLockerStatus (On|Off|Suspended|Unknown) + encryptionPercent
//
// Fonte: Get-BitLockerVolume -MountPoint (módulo BitLocker; presente em Windows
// Pro/Enterprise e disponível para SYSTEM). Fallback: WMI
// Win32_EncryptableVolume (rootcimv2securitymicrosoftvolumeencryption), que
// existe mesmo quando o módulo não está carregado e devolve ConversionStatus
// numérico. Roda como o agente, sem ApplyUserContext (o prompt do usuário não
// pode aparecer no serviço).
//
// Mapeamento (documentado): FullyEncrypted/EncryptionInProgress = On (proteção
// ativa), FullyDecrypted/DecryptionInProgress = Off, qualquer *Paused =
// Suspended, resto = Unknown.
// ─────────────────────────────────────────────────────────────────────────────

// sensorVolumeLetter normaliza a letra do volume alvo (default %SystemDrive%).
func sensorVolumeLetter(cfg *SensorConfig) (string, error) {
	raw := strings.TrimSpace(cfg.Options.String("drive", "disk", "volume", "mountPoint"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("SystemDrive"))
	}
	if raw == "" {
		raw = "C:"
	}
	match := sensorVolumeLetterPattern.FindStringSubmatch(raw)
	if match == nil {
		return "", fmt.Errorf("volume invalido %q (use a letra, ex.: C:)", raw)
	}
	return strings.ToUpper(match[1]) + ":", nil
}

// sensorBitLockerScript consulta o status de UM volume (%s = letra "C:").
const sensorBitLockerScript = `$ErrorActionPreference = 'SilentlyContinue'
$mount = '%s'
$result = $null
try {
  $v = Get-BitLockerVolume -MountPoint $mount -ErrorAction Stop | Select-Object -First 1
  if ($v) { $result = [pscustomobject]@{ status = "$($v.VolumeStatus)"; percent = $v.EncryptionPercentage; protection = $v.ProtectionStatus } }
} catch { }
if (-not $result) {
  try {
    $w = Get-CimInstance -Namespace 'rootcimv2securitymicrosoftvolumeencryption' -ClassName Win32_EncryptableVolume -Filter "DriveLetter='$mount'" -ErrorAction Stop | Select-Object -First 1
    if ($w) { $result = [pscustomobject]@{ status = "$($w.ConversionStatus)"; percent = $w.EncryptionPercentage; protection = $w.ProtectionStatus } }
  } catch { }
}
if ($result) { $result | ConvertTo-Json -Compress } else { '{}' }
`

// sensorBitLockerResult é o JSON do script de BitLocker.
type sensorBitLockerResult struct {
	Status     string   `json:"status"`
	Percent    *float64 `json:"percent"`
	Protection *float64 `json:"protection"`
}

// mapBitLockerStatus traduz o status do Windows para o vocabulário do contrato.
func mapBitLockerStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "fullyencrypted", "encryptioninprogress", "1", "2":
		return "On"
	case "fullydecrypted", "decryptioninprogress", "0", "3":
		return "Off"
	case "encryptionpaused", "decryptionpaused", "4", "5":
		return "Suspended"
	default:
		return "Unknown"
	}
}

// parseBitLockerOutput monta a evidência do BitLocker a partir do JSON.
func parseBitLockerOutput(output string) (sensorCollection, error) {
	var result sensorBitLockerResult
	if err := decodeSensorJSON(output, &result); err != nil {
		return sensorCollection{}, err
	}
	if strings.TrimSpace(result.Status) == "" && result.Percent == nil {
		// Volume não gerenciado pelo BitLocker (ou classe ausente): evidência
		// explícita Unknown — o servidor decide o que fazer.
		return sensorCollection{Measure: SensorMeasure{BitLockerStatus: sensorString("Unknown")}}, nil
	}
	measure := SensorMeasure{BitLockerStatus: sensorString(mapBitLockerStatus(result.Status))}
	if result.Percent != nil {
		percent := *result.Percent
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		measure.BitLockerEncryptionPercent = sensorFloat(roundSensorPercent(percent))
	}
	return sensorCollection{Measure: measure}, nil
}

// collectBitLockerSamples consulta o BitLocker do volume alvo (uma amostra).
func collectBitLockerSamples(cfg *SensorConfig) []sensorCollection {
	letter, err := sensorVolumeLetter(cfg)
	if err != nil {
		return []sensorCollection{{Err: err.Error()}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), sensorPowershellTimeout)
	defer cancel()
	script := fmt.Sprintf(sensorBitLockerScript, letter)
	output, runErr := runSensorPowerShell(ctx, sensorPowershellTimeout, script)
	collection, parseErr := parseBitLockerOutput(output)
	if parseErr != nil {
		if runErr != nil {
			return []sensorCollection{{TargetKey: letter, Err: fmt.Sprintf("falha ao consultar o BitLocker de %s: %v", letter, runErr)}}
		}
		return []sensorCollection{{TargetKey: letter, Err: fmt.Sprintf("saida do BitLocker invalida: %v", parseErr)}}
	}
	collection.TargetKey = letter
	return []sensorCollection{collection}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5) EVENT LOG RATE — eventCount/eventWindowMinutes/lastEventAt
//
// Fonte: Get-WinEvent (PowerShell) com FilterHashtable — o caminho pragmático
// sem dependência nova (a API do Event Log exigiria wevtapi via cgo/winio e o
// parsing de XML do evento; o mesmo dado sai pronto por aqui). O filtro é
// montado SÓ com valores validados (log em allowlist, níveis numéricos), então
// não há interpolação livre no script.
//
// options: logName (default "System"), levels (default [1,2] = Critical/Error),
// windowMinutes (default 60, 1..10080), maxEvents (default 5000).
//
// Limitação: Get-WinEvent enumera os eventos da janela (até maxEvents) em vez de
// usar uma contagem server-side — em log muito movimentado o valor é truncado no
// teto; a evidência continua coerente ("pelo menos N eventos").
// ─────────────────────────────────────────────────────────────────────────────

// sensorEventLogNamePattern aceita nomes de log conhecidos (System, Application,
// "Microsoft-Windows-.../Operational").
var sensorEventLogNamePattern = regexp.MustCompile(`^[A-Za-z0-9 _./\-]+$`)

// sensorEventLogOptions normaliza as opções do sensor de log de eventos.
func sensorEventLogOptions(cfg *SensorConfig) (string, []int, int, int, error) {
	name := strings.TrimSpace(cfg.Options.String("logName", "log", "channel"))
	if name == "" {
		name = "System"
	}
	if !sensorEventLogNamePattern.MatchString(name) {
		return "", nil, 0, 0, fmt.Errorf("logName invalido: %q", name)
	}

	levels := make([]int, 0, 2)
	for _, item := range cfg.Options.Strings("levels", "level") {
		value, convErr := strconv.Atoi(strings.TrimSpace(item))
		if convErr != nil || value < 1 || value > 5 {
			continue
		}
		levels = append(levels, value)
	}
	if len(levels) == 0 {
		if single := cfg.Options.Int(0, "level"); single >= 1 && single <= 5 {
			levels = append(levels, single)
		}
	}
	if len(levels) == 0 {
		levels = []int{1, 2}
	}

	windowMinutes := cfg.Options.Int(sensorDefaultEventLogWindowMinutes, "windowMinutes", "window", "minutes")
	if windowMinutes < 1 {
		windowMinutes = 1
	}
	if windowMinutes > 10080 {
		windowMinutes = 10080
	}

	maxEvents := cfg.Options.Int(sensorMaxEventLogEvents, "maxEvents", "limit", "count")
	if maxEvents < 1 {
		maxEvents = 1
	}
	if maxEvents > sensorMaxEventLogEvents {
		maxEvents = sensorMaxEventLogEvents
	}
	return name, levels, windowMinutes, maxEvents, nil
}

// sensorEventLogScriptTemplate consulta contagem + evento mais recente.
// Ordem do Sprintf: log, janela (min), níveis, maxEvents.
const sensorEventLogScriptTemplate = `$ErrorActionPreference = 'Stop'
$filter = @{ LogName = '%s'; StartTime = (Get-Date).AddMinutes(-%d); Level = @(%s) }
# -ListLog valida o log: 'log nao existe' deve ser ERRO DE COLETA. Janela sem
# eventos é evidência válida (count=0), então a consulta em si é SilentlyContinue
# (Get-WinEvent lança 'No events were found' quando o filtro não casa nada).
$null = Get-WinEvent -ListLog $filter.LogName -ErrorAction Stop
$events = @(Get-WinEvent -FilterHashtable $filter -MaxEvents %d -ErrorAction SilentlyContinue)
if ($events.Count -gt 0) {
  [pscustomobject]@{ count = $events.Count; last = $events[0].TimeCreated.ToUniversalTime().ToString('o') } | ConvertTo-Json -Compress
} else {
  [pscustomobject]@{ count = 0; last = $null } | ConvertTo-Json -Compress
}
`

// sensorEventLogResult é o JSON do script de log de eventos.
type sensorEventLogResult struct {
	Count int     `json:"count"`
	Last  *string `json:"last"`
}

// sensorEventLogScript monta o script com opções já validadas.
func sensorEventLogScript(name string, levels []int, windowMinutes, maxEvents int) string {
	levelList := make([]string, 0, len(levels))
	for _, level := range levels {
		levelList = append(levelList, strconv.Itoa(level))
	}
	return fmt.Sprintf(sensorEventLogScriptTemplate, name, windowMinutes, strings.Join(levelList, ","), maxEvents)
}

// parseEventLogTimestamp normaliza o 'o' do .NET (RFC3339 com 7 casas).
func parseEventLogTimestamp(raw string) (*string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, trimmed)
		if err != nil {
			return nil, err
		}
	}
	formatted := parsed.UTC().Format(time.RFC3339)
	return &formatted, nil
}

// buildEventLogCollection monta a evidência do log de eventos.
func buildEventLogCollection(output string, windowMinutes int) (sensorCollection, error) {
	var result sensorEventLogResult
	if err := decodeSensorJSON(output, &result); err != nil {
		return sensorCollection{}, err
	}
	measure := SensorMeasure{
		EventCount:         sensorInt(result.Count),
		EventWindowMinutes: sensorInt(windowMinutes),
	}
	if result.Last != nil {
		lastAt, err := parseEventLogTimestamp(*result.Last)
		if err != nil {
			return sensorCollection{}, fmt.Errorf("instante do ultimo evento invalido: %w", err)
		}
		measure.LastEventAt = lastAt
	}
	return sensorCollection{Measure: measure}, nil
}

// collectEventLogRateSample conta eventos no log/janela configurados.
func collectEventLogRateSample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	name, levels, windowMinutes, maxEvents, err := sensorEventLogOptions(cfg)
	if err != nil {
		return sensorCollection{Err: err.Error()}
	}
	script := sensorEventLogScript(name, levels, windowMinutes, maxEvents)
	output, runErr := runSensorPowerShell(ctx, sensorCommandTimeout(cfg, sensorPowershellTimeout), script)
	collection, parseErr := buildEventLogCollection(output, windowMinutes)
	if parseErr != nil {
		if runErr != nil {
			return sensorCollection{TargetKey: name, Err: fmt.Sprintf("falha ao consultar o log %s: %v", name, runErr)}
		}
		return sensorCollection{TargetKey: name, Err: fmt.Sprintf("saida do log %s invalida: %v", name, parseErr)}
	}
	collection.TargetKey = name
	return collection
}

// ─────────────────────────────────────────────────────────────────────────────
// 6) DEFENDER STATUS — defenderEnabled/realTime/signatureAgeDays/version
//
// Fonte primária: Get-MpComputerStatus (módulo Defender; roda como SYSTEM).
// Fallback: rootSecurityCenter2 AntiVirusProduct productState, decodificado
// pelo padrão conhecido: bit 0x1000 = antivírus ATIVO e bit 0x10 = proteção em
// tempo real ativa. O fallback não tem versão/idade de assinatura (ficam nil).
//
// Limitação: em servidor com Defender desabilitado por política o cmdlet pode
// devolver valores zerados; o agente só transporta, o servidor decide.
// ─────────────────────────────────────────────────────────────────────────────

// sensorDefenderScript consulta o estado do Defender.
const sensorDefenderScript = `$ErrorActionPreference = 'SilentlyContinue'
$result = $null
try {
  $s = Get-MpComputerStatus -ErrorAction Stop | Select-Object -First 1
  if ($s) {
    $result = [pscustomobject]@{
      enabled          = $s.AntivirusEnabled
      realTime         = $s.RealTimeProtectionEnabled
      signatureAge     = $s.AntivirusSignatureAge
      signatureVersion = "$($s.AntivirusSignatureVersion)"
      source           = 'defender'
    }
  }
} catch { }
if (-not $result) {
  try {
    $p = Get-CimInstance -Namespace 'rootSecurityCenter2' -ClassName AntiVirusProduct -ErrorAction Stop | Select-Object -First 1
    if ($p) { $result = [pscustomobject]@{ productState = [int]$p.productState; source = 'securitycenter2' } }
  } catch { }
}
if ($result) { $result | ConvertTo-Json -Compress } else { '{}' }
`

// sensorDefenderResult é o JSON do script do Defender.
type sensorDefenderResult struct {
	Enabled      *bool    `json:"enabled"`
	RealTime     *bool    `json:"realTime"`
	SignatureAge *float64 `json:"signatureAge"`
	SignatureVer string   `json:"signatureVersion"`
	ProductState *int     `json:"productState"`
	Source       string   `json:"source"`
}

// defenderEnabledFromProductState decodifica o productState do SecurityCenter2:
// o bit 0x1000 marca antivírus ATIVO (o segundo nibble é o estado da assinatura,
// não a proteção em tempo real — por isso o fallback NÃO reporta realTime, que
// fica nil como "sem dado" em vez de um booleano inventado).
func defenderEnabledFromProductState(state int) bool {
	return state&0x1000 != 0
}

// parseDefenderOutput monta a evidência do Defender.
func parseDefenderOutput(output string) (sensorCollection, error) {
	var result sensorDefenderResult
	if err := decodeSensorJSON(output, &result); err != nil {
		return sensorCollection{}, err
	}
	measure := SensorMeasure{}
	if result.ProductState != nil && result.Enabled == nil {
		measure.DefenderEnabled = sensorBool(defenderEnabledFromProductState(*result.ProductState))
		return sensorCollection{Measure: measure}, nil
	}
	if result.Enabled == nil && result.RealTime == nil {
		// Nenhum antivírus reportado: evidência vazia é "sem dado", não "Off".
		return sensorCollection{Measure: SensorMeasure{}}, nil
	}
	if result.Enabled != nil {
		measure.DefenderEnabled = sensorBool(*result.Enabled)
	}
	if result.RealTime != nil {
		measure.DefenderRealTimeEnabled = sensorBool(*result.RealTime)
	}
	if result.SignatureAge != nil {
		age := *result.SignatureAge
		if age < 0 {
			age = 0
		}
		measure.SignatureAgeDays = sensorFloat(sensorRound2(age))
	}
	if version := strings.TrimSpace(result.SignatureVer); version != "" {
		measure.SignatureVersion = sensorString(version)
	}
	return sensorCollection{Measure: measure}, nil
}

// collectDefenderSample lê o estado do antivírus (uma amostra).
func collectDefenderSample(ctx context.Context) sensorCollection {
	output, runErr := runSensorPowerShell(ctx, sensorPowershellTimeout, sensorDefenderScript)
	collection, parseErr := parseDefenderOutput(output)
	if parseErr != nil {
		if runErr != nil {
			return sensorCollection{Err: fmt.Sprintf("falha ao consultar o Defender: %v", runErr)}
		}
		return sensorCollection{Err: fmt.Sprintf("saida do Defender invalida: %v", parseErr)}
	}
	return collection
}

// ─────────────────────────────────────────────────────────────────────────────
// 7) CERTIFICATE EXPIRY — daysToExpiry/expiresAt/certificateSubject
//
// Alvo "host" ou "host:porta" (options.port default 443). Conecta via tls.Dial,
// lê o certificado FOLHA do handshake e reporta a validade. A verificação da
// cadeia é DESLIGADA de propósito: o sensor mede VALIDADE (evidência), não
// confiança — e o contrato não tem campo de "cadeia ok"; além disso, hosts
// internos usam CA própria e um erro de cadeia viraria "erro de coleta" em vez
// de "certificado vence em N dias". Limitação documentada.
// ─────────────────────────────────────────────────────────────────────────────

// sensorCertificateAddress monta host:porta e o targetKey.
func sensorCertificateAddress(cfg *SensorConfig) (string, string, error) {
	target := strings.TrimSpace(cfg.Policy.Target)
	if target == "" {
		return "", "", fmt.Errorf("sensor de certificado sem alvo (host ou host:porta)")
	}
	host, port, splitErr := net.SplitHostPort(target)
	if splitErr == nil && host != "" && port != "" {
		if _, convErr := strconv.Atoi(port); convErr != nil {
			return "", "", fmt.Errorf("porta invalida no alvo %q", target)
		}
		return target, target, nil
	}
	host = strings.Trim(target, "[]")
	port = strconv.Itoa(cfg.Options.Int(sensorDefaultCertificatePort, "port", "porta"))
	value, convErr := strconv.Atoi(port)
	if convErr != nil || value <= 0 || value > 65535 {
		return "", "", fmt.Errorf("porta TLS invalida no optionsJson: %q", port)
	}
	address := net.JoinHostPort(host, port)
	return address, address, nil
}

// collectCertificateExpirySample conecta via TLS e reporta a validade do leaf.
func collectCertificateExpirySample(ctx context.Context, cfg *SensorConfig) sensorCollection {
	address, targetKey, err := sensorCertificateAddress(cfg)
	if err != nil {
		return sensorCollection{Err: err.Error()}
	}
	timeout := sensorCommandTimeout(cfg, 20*time.Second)
	host, _, _ := net.SplitHostPort(address)

	dialer := &net.Dialer{Timeout: timeout}
	connection, dialErr := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{
		ServerName: host,
		// Comentário do bloco: medimos validade, não confiança na cadeia.
		InsecureSkipVerify: true, //nolint:gosec // sensor de validade, sem validacao de cadeia
		MinVersion:         tls.VersionTLS10,
	})
	if dialErr != nil {
		if ctx.Err() != nil {
			return sensorCollection{TargetKey: targetKey, Measure: SensorMeasure{TimedOut: true}, Err: fmt.Sprintf("tempo esgotado ao conectar em %s", address)}
		}
		return sensorCollection{TargetKey: targetKey, Err: fmt.Sprintf("falha ao conectar em %s: %v", address, dialErr)}
	}
	defer connection.Close()

	state := connection.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return sensorCollection{TargetKey: targetKey, Err: fmt.Sprintf("%s nao apresentou certificado no handshake", address)}
	}
	leaf := state.PeerCertificates[0]
	daysToExpiry := time.Until(leaf.NotAfter).Hours() / 24
	return sensorCollection{TargetKey: targetKey, Measure: SensorMeasure{
		DaysToExpiry:       sensorFloat(sensorRound2(daysToExpiry)),
		ExpiresAt:          sensorString(leaf.NotAfter.UTC().Format(time.RFC3339)),
		CertificateSubject: sensorString(leaf.Subject.String()),
	}}
}

// ─────────────────────────────────────────────────────────────────────────────
// 8) SERVIÇO — restartIfStopped
//
// Quando o serviço está Stopped e options.restartIfStopped=true: tenta Start no
// SCM, espera options.restartWaitMs (default 5000, teto 30000) reconsultando o
// estado, e envia RestartAttempted/RestartSucceeded + o estado FINAL. Falhar ao
// iniciar NÃO é erro de coleta: a evidência (tentou/ não subiu / estado) é o que
// o servidor precisa para decidir Failing + alerta.
//
// A dependência de SCM é uma INTERFACE (sensorServiceController) para o teste
// cobrir o fluxo de restart com um serviço FAKE, sem tocar em serviço real.
// ─────────────────────────────────────────────────────────────────────────────

// sensorServiceController é a dependência de SCM do coletor de serviço.
type sensorServiceController interface {
	Query(ctx context.Context, name string) (state string, running bool, err error)
	Start(ctx context.Context, name string) error
}

// scmSensorServiceController implementa sensorServiceController com o SCM.
type scmSensorServiceController struct{}

func (scmSensorServiceController) Query(ctx context.Context, name string) (string, bool, error) {
	return queryServiceState(ctx, name)
}

func (scmSensorServiceController) Start(ctx context.Context, name string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("nao foi possivel conectar ao SCM: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if err != nil {
		return fmt.Errorf("nao foi possivel abrir o servico %q: %w", name, err)
	}
	defer service.Close()
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("falha ao iniciar o servico %q: %w", name, err)
	}
	return nil
}

// restartStoppedServiceWith executa a tentativa de restart com o controlador
// informado (o SCM em produção, fake nos testes).
func restartStoppedServiceWith(ctx context.Context, cfg *SensorConfig, name string, controller sensorServiceController) sensorCollection {
	measure := SensorMeasure{RestartAttempted: sensorBool(true)}
	// O erro de Start é intencionalmente absorvido: o estado final é a evidência.
	_ = controller.Start(ctx, name)

	waitMs := cfg.Options.Int(sensorDefaultRestartWaitMs, "restartWaitMs", "restartWait", "waitMs")
	if waitMs < 0 {
		waitMs = 0
	}
	if waitMs > sensorMaxRestartWaitMs {
		waitMs = sensorMaxRestartWaitMs
	}
	deadline := time.Now().Add(time.Duration(waitMs) * time.Millisecond)

	finalState, finalRunning := "Stopped", false
	for {
		if state, running, err := controller.Query(ctx, name); err == nil {
			finalState, finalRunning = state, running
		}
		if finalRunning || !time.Now().Before(deadline) {
			break
		}
		if !sensorWaitContext(ctx, 250) {
			break
		}
	}
	measure.ServiceState = sensorString(finalState)
	measure.Running = sensorBool(finalRunning)
	measure.RestartSucceeded = sensorBool(finalRunning && finalState == "Running")
	return sensorCollection{Measure: measure}
}
