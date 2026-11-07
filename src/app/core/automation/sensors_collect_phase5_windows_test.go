//go:build windows

package automation

// ─────────────────────────────────────────────────────────────────────────────
// Testes dos coletores da FASE 5 (Windows).
//
// Todos os testes são de PARSING/MAPEAMENTO (funções puras) ou usam um servidor
// TLS local / serviço FAKE: nenhum depende de disco físico, WMI ou rede externa,
// então não há flake de ambiente no CI.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── Tipos / contrato ──

func TestSensorTypeByNumberFase5(t *testing.T) {
	cases := []struct {
		number int
		want   SensorType
	}{
		{14, SensorTypeEventLogRate},
		{15, SensorTypeSmartStatus},
		{16, SensorTypeCertificate},
		{17, SensorTypeDefenderStatus},
		{18, SensorTypeBitLocker},
		// F5: ADConsistency = 19 no servidor (SensorType.ADConsistency).
		{19, SensorTypeADConsistency},
	}
	for _, testCase := range cases {
		got, ok := sensorTypeByNumber[testCase.number]
		if !ok || got != testCase.want {
			t.Fatalf("sensorTypeByNumber[%d] = %q (ok=%v), want %q", testCase.number, got, ok, testCase.want)
		}
	}
	var parsed SensorType
	if err := parsed.UnmarshalJSON([]byte("15")); err != nil || parsed != SensorTypeSmartStatus {
		t.Fatalf("UnmarshalJSON(15) = %q, %v; want SmartStatus", parsed, err)
	}
}

// ── Bandwidth ──

// buildSensorIfRowBuffer monta um buffer GetIfTable sintético (header + rows).
func buildSensorIfRowBuffer(rows []sensorIfRow) []byte {
	buffer := make([]byte, 4+len(rows)*sensorMibIfRowSize)
	binary.LittleEndian.PutUint32(buffer[:4], uint32(len(rows)))
	for index, row := range rows {
		offset := 4 + index*sensorMibIfRowSize
		target := buffer[offset:]
		for charIndex, char := range row.Name {
			if charIndex*2+2 > 512 {
				break
			}
			binary.LittleEndian.PutUint16(target[charIndex*2:charIndex*2+2], uint16(char))
		}
		binary.LittleEndian.PutUint32(target[512:516], row.Index)
		binary.LittleEndian.PutUint32(target[516:520], row.Type)
		binary.LittleEndian.PutUint32(target[544:548], row.OperStatus)
		binary.LittleEndian.PutUint32(target[552:556], row.InOctets)
		binary.LittleEndian.PutUint32(target[576:580], row.OutOctets)
	}
	return buffer
}

func TestParseSensorIfTable(t *testing.T) {
	buffer := buildSensorIfRowBuffer([]sensorIfRow{
		{Name: "Loopback", Index: 1, Type: ifTypeSoftwareLoopback, OperStatus: ifOperStatusOperational},
		{Name: "Ethernet", Index: 2, Type: 6, OperStatus: ifOperStatusConnected, InOctets: 1000, OutOctets: 2000},
	})
	rows := parseSensorIfTable(buffer)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Name != "Loopback" || rows[0].Index != 1 || rows[0].Type != ifTypeSoftwareLoopback {
		t.Fatalf("row 0 = %+v", rows[0])
	}
	if rows[1].Name != "Ethernet" || rows[1].InOctets != 1000 || rows[1].OutOctets != 2000 {
		t.Fatalf("row 1 = %+v", rows[1])
	}
	if sensorIfRowEligible(rows[0]) {
		t.Fatal("loopback deveria ser inelegivel")
	}
	if !sensorIfRowEligible(rows[1]) {
		t.Fatal("ethernet conectada deveria ser elegivel")
	}
}

func TestParseSensorIfTableTruncated(t *testing.T) {
	if rows := parseSensorIfTable([]byte{1, 2}); rows != nil {
		t.Fatalf("buffer curto = %+v, want nil", rows)
	}
	if rows := parseSensorIfTable(nil); rows != nil {
		t.Fatalf("buffer nil = %+v, want nil", rows)
	}
	// Cabeçalho diz 3, só cabe 1: para na primeira que não cabe.
	buffer := buildSensorIfRowBuffer([]sensorIfRow{{Name: "A", Index: 9, Type: 6, OperStatus: ifOperStatusConnected}})
	binary.LittleEndian.PutUint32(buffer[:4], 3)
	if rows := parseSensorIfTable(buffer); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (truncado para o que cabe)", len(rows))
	}
}

func TestSensorCounterDeltaBps(t *testing.T) {
	cases := []struct {
		name     string
		previous uint64
		current  uint64
		seconds  float64
		want     float64
	}{
		{name: "delta normal", previous: 1000, current: 3000, seconds: 2, want: 1000},
		{name: "contador resetado", previous: 3000, current: 1000, seconds: 2, want: 0},
		{name: "janela invalida", previous: 0, current: 500, seconds: 0, want: 0},
		{name: "sem trafego", previous: 500, current: 500, seconds: 1, want: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := sensorCounterDeltaBps(testCase.previous, testCase.current, testCase.seconds); got != testCase.want {
				t.Fatalf("delta = %v, want %v", got, testCase.want)
			}
		})
	}
}

// ── DiskIo ──

func TestParseSensorPhysicalDriveNumber(t *testing.T) {
	cases := []struct {
		raw  string
		want uint32
		ok   bool
	}{
		{raw: `\\.\PhysicalDrive2`, want: 2, ok: true},
		{raw: "PhysicalDrive0", want: 0, ok: true},
		{raw: "3", want: 3, ok: true},
		{raw: "C:", ok: false},
		{raw: "", ok: false},
		{raw: "Samsung SSD", ok: false},
	}
	for _, testCase := range cases {
		got, ok := parseSensorPhysicalDriveNumber(testCase.raw)
		if ok != testCase.ok || (ok && got != testCase.want) {
			t.Fatalf("parse(%q) = %d, %v; want %d, %v", testCase.raw, got, ok, testCase.want, testCase.ok)
		}
	}
}

func TestComputeDiskIOMetrics(t *testing.T) {
	previous := sensorDiskPerformance{BytesRead: 1_000_000, BytesWritten: 500_000, IdleTime: 10_000_000, ReadTime: 1_000_000, WriteTime: 500_000, ReadCount: 10, WriteCount: 5}
	current := sensorDiskPerformance{BytesRead: 3_000_000, BytesWritten: 1_500_000, IdleTime: 30_000_000, ReadTime: 3_000_000, WriteTime: 1_500_000, ReadCount: 30, WriteCount: 15}

	readBps, writeBps, busy, latency, hasBusy, hasLatency := computeDiskIOMetrics(previous, current, 2)
	if readBps != 1_000_000 || writeBps != 500_000 {
		t.Fatalf("readBps=%v writeBps=%v, want 1e6/5e5", readBps, writeBps)
	}
	if !hasBusy {
		t.Fatal("busy deveria existir com IdleTime preenchido")
	}
	// idle delta = 20s em janela de 2s → limitado a 0% ocupado.
	if busy != 0 {
		t.Fatalf("busyPercent = %v, want 0 (idle > janela)", busy)
	}
	if !hasLatency {
		t.Fatal("latency deveria existir com transferencias")
	}
	// serviceTime = 2e6 + 1e6 = 3e6 (100ns) / 30 transferencias / 1e4 = 10ms
	if latency != 10 {
		t.Fatalf("latencyMs = %v, want 10", latency)
	}

	t.Run("sem IdleTime nao inventa busy", func(t *testing.T) {
		_, _, busy, _, hasBusy, _ := computeDiskIOMetrics(sensorDiskPerformance{}, sensorDiskPerformance{BytesRead: 10}, 1)
		if hasBusy || busy != 0 {
			t.Fatalf("busy = %v (hasBusy=%v), want ausente", busy, hasBusy)
		}
	})
	t.Run("sem transferencias nao inventa latencia", func(t *testing.T) {
		_, _, _, _, _, hasLatency := computeDiskIOMetrics(sensorDiskPerformance{}, sensorDiskPerformance{BytesRead: 10}, 1)
		if hasLatency {
			t.Fatal("latency deveria estar ausente sem transferencias")
		}
	})
	t.Run("contador regredido vira zero", func(t *testing.T) {
		readBps, _, _, _, _, _ := computeDiskIOMetrics(sensorDiskPerformance{BytesRead: 100}, sensorDiskPerformance{BytesRead: 10}, 1)
		if readBps != 0 {
			t.Fatalf("readBps = %v, want 0", readBps)
		}
	})
}

// ── SMART ──

// buildSmartBlock monta um bloco SMART de 512 bytes com os atributos pedidos.
func buildSmartBlock(attributes map[byte][6]byte) []byte {
	block := make([]byte, 512)
	block[0] = 0x10
	block[1] = 0x00
	index := 0
	for id, raw := range attributes {
		offset := 2 + index*12
		block[offset] = id
		block[offset+1] = 0x00
		block[offset+2] = 0x00
		block[offset+3] = 100
		block[offset+4] = 100
		copy(block[offset+5:offset+11], raw[:])
		index++
	}
	return block
}

func TestParseSmartAttributes(t *testing.T) {
	block := buildSmartBlock(map[byte][6]byte{
		5:   {0x0A, 0x00, 0x00, 0x00, 0x00, 0x00}, // 10 setores realocados
		9:   {0xD2, 0x04, 0x00, 0x00, 0x00, 0x00}, // 1234 horas
		194: {45, 0, 0, 0, 0, 0},                  // 45 °C
	})
	reallocated, temperature, powerOnHours, ok := parseSmartAttributes(block)
	if !ok {
		t.Fatal("parseSmartAttributes deveria reconhecer atributos")
	}
	if reallocated == nil || *reallocated != 10 {
		t.Fatalf("reallocated = %v, want 10", reallocated)
	}
	if powerOnHours == nil || *powerOnHours != 1234 {
		t.Fatalf("powerOnHours = %v, want 1234", powerOnHours)
	}
	if temperature == nil || *temperature != 45 {
		t.Fatalf("temperature = %v, want 45", temperature)
	}
	if _, _, _, ok := parseSmartAttributes(make([]byte, 100)); ok {
		t.Fatal("bloco curto nao deveria ser reconhecido")
	}
}

func TestWriteSmartReportParsing(t *testing.T) {
	block := buildSmartBlock(map[byte][6]byte{5: {0x02, 0, 0, 0, 0, 0}})
	encoded := base64.StdEncoding.EncodeToString(block)
	output := `{"disks":[{"deviceId":"0","name":"Disco A","health":"Healthy","temperature":41.5,"powerOnHours":900},{"deviceId":"1","name":"Disco B","health":"Warning","temperature":null,"powerOnHours":null}],"smart":[{"instance":"IDE\\DiskDiscoA_0","data":"` + encoded + `"}]}`
	report, byDisk, err := parseSmartReport(output)
	if err != nil {
		t.Fatalf("parseSmartReport: %v", err)
	}
	if len(report.Disks) != 2 || len(byDisk) != 1 {
		t.Fatalf("disks=%d byDisk=%d", len(report.Disks), len(byDisk))
	}
	if _, found := byDisk[0]; !found {
		t.Fatal("SMART do PhysicalDrive0 deveria estar mapeado")
	}
}

func TestWorstSmartStatus(t *testing.T) {
	cases := []struct {
		name  string
		disks []sensorPhysicalDiskInfo
		want  string
	}{
		{name: "vazio e unknown", want: "Unknown"},
		{name: "todos saudaveis", disks: []sensorPhysicalDiskInfo{{Health: "Healthy"}, {Health: "Healthy"}}, want: "Healthy"},
		{name: "warning vence healthy", disks: []sensorPhysicalDiskInfo{{Health: "Healthy"}, {Health: "Warning"}}, want: "Warning"},
		{name: "unhealthy vence todos", disks: []sensorPhysicalDiskInfo{{Health: "Healthy"}, {Health: "Warning"}, {Health: "Unhealthy"}}, want: "Unhealthy"},
		{name: "health vazio vira unknown", disks: []sensorPhysicalDiskInfo{{Health: ""}}, want: "Unknown"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := worstSmartStatus(testCase.disks); got != testCase.want {
				t.Fatalf("worstSmartStatus = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestBuildSmartStatusCollection(t *testing.T) {
	block := buildSmartBlock(map[byte][6]byte{5: {0x03, 0, 0, 0, 0, 0}})
	smartByDisk := map[uint32]sensorSmartVendorData{1: {Instance: "IDE\\DiskB_1", Data: base64.StdEncoding.EncodeToString(block)}}
	disks := []sensorPhysicalDiskInfo{
		{DeviceID: "0", Name: "Disco A", Health: "Healthy", Temperature: sensorFloat(40), PowerOnHours: sensorFloat(100)},
		{DeviceID: "1", Name: "Disco B", Health: "Warning", Temperature: sensorFloat(55), PowerOnHours: sensorFloat(200)},
	}
	collection := buildSmartStatusCollection(disks, smartByDisk, "")
	if collection.Err != "" {
		t.Fatalf("erro inesperado: %s", collection.Err)
	}
	if collection.TargetKey != "" {
		t.Fatalf("targetKey = %q, want vazio (todos os discos)", collection.TargetKey)
	}
	if collection.Measure.SmartStatus == nil || *collection.Measure.SmartStatus != "Warning" {
		t.Fatalf("smartStatus = %v, want Warning", collection.Measure.SmartStatus)
	}
	if collection.Measure.TemperatureC == nil || *collection.Measure.TemperatureC != 55 {
		t.Fatalf("temperatureC = %v, want 55 (maior)", collection.Measure.TemperatureC)
	}
	if collection.Measure.PowerOnHours == nil || *collection.Measure.PowerOnHours != 200 {
		t.Fatalf("powerOnHours = %v, want 200 (maior)", collection.Measure.PowerOnHours)
	}
	if collection.Measure.ReallocatedSectors == nil || *collection.Measure.ReallocatedSectors != 3 {
		t.Fatalf("reallocatedSectors = %v, want 3 (do SMART)", collection.Measure.ReallocatedSectors)
	}

	t.Run("alvo especifico usa targetKey do disco", func(t *testing.T) {
		single := buildSmartStatusCollection(disks, nil, `\\.\PhysicalDrive1`)
		if single.Err != "" {
			t.Fatalf("erro inesperado: %s", single.Err)
		}
		if single.TargetKey != "Disco B" {
			t.Fatalf("targetKey = %q, want Disco B", single.TargetKey)
		}
	})
	t.Run("alvo inexistente vira erro explicito", func(t *testing.T) {
		missing := buildSmartStatusCollection(disks, nil, "PhysicalDrive9")
		if missing.Err == "" {
			t.Fatal("alvo inexistente deveria produzir erro de coleta")
		}
	})
}

// ── BitLocker ──

func TestMapBitLockerStatus(t *testing.T) {
	cases := map[string]string{
		"FullyEncrypted":       "On",
		"EncryptionInProgress": "On",
		"1":                    "On",
		"2":                    "On",
		"FullyDecrypted":       "Off",
		"DecryptionInProgress": "Off",
		"0":                    "Off",
		"3":                    "Off",
		"EncryptionPaused":     "Suspended",
		"DecryptionPaused":     "Suspended",
		"4":                    "Suspended",
		"5":                    "Suspended",
		"":                     "Unknown",
		"qualquer":             "Unknown",
	}
	for raw, want := range cases {
		if got := mapBitLockerStatus(raw); got != want {
			t.Fatalf("mapBitLockerStatus(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestParseBitLockerOutput(t *testing.T) {
	t.Run("criptografado 100%", func(t *testing.T) {
		collection, err := parseBitLockerOutput(`{"status":"FullyEncrypted","percent":100}`)
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if collection.Measure.BitLockerStatus == nil || *collection.Measure.BitLockerStatus != "On" {
			t.Fatalf("status = %v, want On", collection.Measure.BitLockerStatus)
		}
		if collection.Measure.BitLockerEncryptionPercent == nil || *collection.Measure.BitLockerEncryptionPercent != 100 {
			t.Fatalf("percent = %v, want 100", collection.Measure.BitLockerEncryptionPercent)
		}
	})
	t.Run("suspenso", func(t *testing.T) {
		collection, err := parseBitLockerOutput(`{"status":"EncryptionPaused","percent":42.5}`)
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if collection.Measure.BitLockerStatus == nil || *collection.Measure.BitLockerStatus != "Suspended" {
			t.Fatalf("status = %v, want Suspended", collection.Measure.BitLockerStatus)
		}
	})
	t.Run("volume desconhecido", func(t *testing.T) {
		collection, err := parseBitLockerOutput("{}")
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if collection.Measure.BitLockerStatus == nil || *collection.Measure.BitLockerStatus != "Unknown" {
			t.Fatalf("status = %v, want Unknown", collection.Measure.BitLockerStatus)
		}
	})
	t.Run("saida invalida", func(t *testing.T) {
		if _, err := parseBitLockerOutput("nao e json"); err == nil {
			t.Fatal("saida nao-JSON deveria virar erro")
		}
	})
}

func TestSensorVolumeLetter(t *testing.T) {
	cases := []struct {
		options string
		want    string
		wantErr bool
	}{
		{options: "", want: "C:"},
		{options: `{"drive":"d:"}`, want: "D:"},
		{options: `{"drive":"e"}`, want: "E:"},
		{options: `{"drive":"\\\\?\\Volume{1}"}`, wantErr: true},
	}
	for _, testCase := range cases {
		cfg := newSensorCollectConfig(SensorTypeBitLocker, "", testCase.options, 5*time.Second)
		got, err := sensorVolumeLetter(&cfg)
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("options %q deveria falhar", testCase.options)
			}
			continue
		}
		if err != nil || got != testCase.want {
			t.Fatalf("options %q = %q, %v; want %q", testCase.options, got, err, testCase.want)
		}
	}
}

// ── Event log ──

func TestSensorEventLogOptionsDefaults(t *testing.T) {
	cfg := newSensorCollectConfig(SensorTypeEventLogRate, "", "", time.Second)
	name, levels, window, maxEvents, err := sensorEventLogOptions(&cfg)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if name != "System" || window != 60 || maxEvents != sensorMaxEventLogEvents {
		t.Fatalf("defaults = %q/%d/%d", name, window, maxEvents)
	}
	if len(levels) != 2 || levels[0] != 1 || levels[1] != 2 {
		t.Fatalf("levels = %v, want [1 2]", levels)
	}
}

func TestSensorEventLogOptionsCustom(t *testing.T) {
	cfg := newSensorCollectConfig(SensorTypeEventLogRate, "", `{"logName":"Application","levels":[2,3],"windowMinutes":15,"maxEvents":100}`, time.Second)
	name, levels, window, maxEvents, err := sensorEventLogOptions(&cfg)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if name != "Application" || window != 15 || maxEvents != 100 {
		t.Fatalf("options = %q/%d/%d", name, window, maxEvents)
	}
	if len(levels) != 2 || levels[0] != 2 || levels[1] != 3 {
		t.Fatalf("levels = %v, want [2 3]", levels)
	}
}

func TestSensorEventLogOptionsInvalidLogName(t *testing.T) {
	cfg := newSensorCollectConfig(SensorTypeEventLogRate, "", `{"logName":"System'; Remove-Item -Recurse C:\\"}`, time.Second)
	if _, _, _, _, err := sensorEventLogOptions(&cfg); err == nil {
		t.Fatal("nome de log com aspas/pontuacao deveria ser rejeitado")
	}
}

func TestSensorEventLogScript(t *testing.T) {
	script := sensorEventLogScript("System", []int{1, 2}, 60, 5000)
	for _, want := range []string{"LogName = 'System'", "AddMinutes(-60)", "Level = @(1,2)", "MaxEvents 5000", "Get-WinEvent -ListLog"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script sem %q: %s", want, script)
		}
	}
}

func TestBuildEventLogCollection(t *testing.T) {
	collection, err := buildEventLogCollection(`{"count":3,"last":"2026-01-02T03:04:05.1234567Z"}`, 60)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if collection.Measure.EventCount == nil || *collection.Measure.EventCount != 3 {
		t.Fatalf("eventCount = %v, want 3", collection.Measure.EventCount)
	}
	if collection.Measure.EventWindowMinutes == nil || *collection.Measure.EventWindowMinutes != 60 {
		t.Fatalf("eventWindowMinutes = %v, want 60", collection.Measure.EventWindowMinutes)
	}
	if collection.Measure.LastEventAt == nil || *collection.Measure.LastEventAt != "2026-01-02T03:04:05Z" {
		t.Fatalf("lastEventAt = %v, want 2026-01-02T03:04:05Z", collection.Measure.LastEventAt)
	}

	t.Run("sem eventos nao tem lastEventAt", func(t *testing.T) {
		empty, err := buildEventLogCollection(`{"count":0,"last":null}`, 10)
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if empty.Measure.EventCount == nil || *empty.Measure.EventCount != 0 {
			t.Fatalf("eventCount = %v, want 0", empty.Measure.EventCount)
		}
		if empty.Measure.LastEventAt != nil {
			t.Fatalf("lastEventAt = %v, want nil", empty.Measure.LastEventAt)
		}
	})
	t.Run("timestamp invalido vira erro", func(t *testing.T) {
		if _, err := buildEventLogCollection(`{"count":1,"last":"ontem"}`, 10); err == nil {
			t.Fatal("timestamp invalido deveria virar erro")
		}
	})
}

// ── Defender ──

func TestDefenderEnabledFromProductState(t *testing.T) {
	cases := []struct {
		state int
		want  bool
	}{
		{state: 0x1010, want: true},  // ligado + assinatura em dia
		{state: 0x1000, want: true},  // ligado + assinatura desatualizada
		{state: 0x0410, want: false}, // sem o bit 0x1000 = desligado
		{state: 0, want: false},
	}
	for _, testCase := range cases {
		if got := defenderEnabledFromProductState(testCase.state); got != testCase.want {
			t.Fatalf("productState 0x%X = %v, want %v", testCase.state, got, testCase.want)
		}
	}
}

func TestParseDefenderOutput(t *testing.T) {
	t.Run("get-mpcomputerstatus", func(t *testing.T) {
		collection, err := parseDefenderOutput(`{"enabled":true,"realTime":true,"signatureAge":2.5,"signatureVersion":"1.400.1.0"}`)
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if collection.Measure.DefenderEnabled == nil || !*collection.Measure.DefenderEnabled {
			t.Fatalf("defenderEnabled = %v, want true", collection.Measure.DefenderEnabled)
		}
		if collection.Measure.DefenderRealTimeEnabled == nil || !*collection.Measure.DefenderRealTimeEnabled {
			t.Fatalf("realTime = %v, want true", collection.Measure.DefenderRealTimeEnabled)
		}
		if collection.Measure.SignatureAgeDays == nil || *collection.Measure.SignatureAgeDays != 2.5 {
			t.Fatalf("signatureAgeDays = %v, want 2.5", collection.Measure.SignatureAgeDays)
		}
		if collection.Measure.SignatureVersion == nil || *collection.Measure.SignatureVersion != "1.400.1.0" {
			t.Fatalf("signatureVersion = %v", collection.Measure.SignatureVersion)
		}
	})
	t.Run("fallback securitycenter2", func(t *testing.T) {
		// 0x1010 = antivírus ligado; realTime fica nil (o productState nao diz).
		collection, err := parseDefenderOutput(`{"productState":4112}`)
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if collection.Measure.DefenderEnabled == nil || !*collection.Measure.DefenderEnabled {
			t.Fatalf("enabled = %v, want true", collection.Measure.DefenderEnabled)
		}
		if collection.Measure.DefenderRealTimeEnabled != nil {
			t.Fatalf("realTime = %v, want nil no fallback", collection.Measure.DefenderRealTimeEnabled)
		}
	})
	t.Run("sem antivirus reportado", func(t *testing.T) {
		collection, err := parseDefenderOutput("{}")
		if err != nil {
			t.Fatalf("erro: %v", err)
		}
		if collection.Measure.DefenderEnabled != nil {
			t.Fatalf("defenderEnabled = %v, want nil (sem dado)", collection.Measure.DefenderEnabled)
		}
	})
}

// ── Certificado ──

func TestSensorCertificateAddress(t *testing.T) {
	cases := []struct {
		target  string
		options string
		want    string
		wantErr bool
	}{
		{target: "example.com", want: "example.com:443"},
		{target: "example.com:8443", want: "example.com:8443"},
		{target: "example.com", options: `{"port":9443}`, want: "example.com:9443"},
		{target: "", wantErr: true},
		{target: "example.com:abc", wantErr: true},
	}
	for _, testCase := range cases {
		cfg := newSensorCollectConfig(SensorTypeCertificate, testCase.target, testCase.options, 5*time.Second)
		got, _, err := sensorCertificateAddress(&cfg)
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("alvo %q deveria falhar", testCase.target)
			}
			continue
		}
		if err != nil || got != testCase.want {
			t.Fatalf("alvo %q = %q, %v; want %q", testCase.target, got, err, testCase.want)
		}
	}
}

// TestCollectCertificateExpiryLocal usa um servidor TLS LOCAL (certificado
// autoassinado gerado pelo httptest): valida daysToExpiry/expiresAt/subject sem
// depender de internet.
func TestCollectCertificateExpiryLocal(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	// httptest.NewTLSServer(nil) não registra handler; só o handshake importa.
	address := server.Listener.Addr().String()
	cfg := newSensorCollectConfig(SensorTypeCertificate, address, "", 5*time.Second)
	collection := collectCertificateExpirySample(context.Background(), &cfg)
	if collection.Err != "" {
		t.Fatalf("erro de coleta: %s", collection.Err)
	}
	if collection.TargetKey != address {
		t.Fatalf("targetKey = %q, want %q", collection.TargetKey, address)
	}
	if collection.Measure.DaysToExpiry == nil {
		t.Fatal("daysToExpiry ausente")
	}
	certificate := server.Certificate()
	wantDays := time.Until(certificate.NotAfter).Hours() / 24
	gotDays := *collection.Measure.DaysToExpiry
	if difference := gotDays - wantDays; difference > 1 || difference < -1 {
		t.Fatalf("daysToExpiry = %v, want ~%v (tolerancia 1 dia)", gotDays, wantDays)
	}
	if gotDays <= 0 {
		t.Fatalf("daysToExpiry = %v, want positivo (certificado do httptest e valido)", gotDays)
	}
	if collection.Measure.ExpiresAt == nil || *collection.Measure.ExpiresAt == "" {
		t.Fatal("expiresAt ausente")
	}
	if collection.Measure.CertificateSubject == nil || *collection.Measure.CertificateSubject == "" {
		t.Fatal("certificateSubject ausente")
	}
}

func TestCollectCertificateExpiryWithoutTarget(t *testing.T) {
	cfg := newSensorCollectConfig(SensorTypeCertificate, "  ", "", 5*time.Second)
	collection := collectCertificateExpirySample(context.Background(), &cfg)
	if collection.Err == "" {
		t.Fatal("sem alvo deveria ser erro de coleta")
	}
}

// ── Serviço: restartIfStopped ──

// fakeSensorService é o SCM FAKE do teste de restart: nenhum serviço real é
// tocado e o estado final pode ser controlado.
type fakeSensorService struct {
	state    string
	running  bool
	startErr error
	// keepStopped simula um Start que "aceita" o comando mas não sobe o serviço.
	keepStopped bool
	startHits   int
	queryHits   int
}

func (fake *fakeSensorService) Query(_ context.Context, _ string) (string, bool, error) {
	fake.queryHits++
	return fake.state, fake.running, nil
}

func (fake *fakeSensorService) Start(_ context.Context, _ string) error {
	fake.startHits++
	if fake.startErr != nil {
		return fake.startErr
	}
	if !fake.keepStopped {
		fake.state = "Running"
		fake.running = true
	}
	return nil
}

func TestRestartStoppedServiceSucceeds(t *testing.T) {
	fake := &fakeSensorService{state: "Stopped"}
	cfg := newSensorCollectConfig(SensorTypeService, "fake-service", `{"restartIfStopped":true,"restartWaitMs":2000}`, 10*time.Second)
	collection := restartStoppedServiceWith(context.Background(), &cfg, "fake-service", fake)

	if fake.startHits != 1 {
		t.Fatalf("startHits = %d, want 1", fake.startHits)
	}
	if collection.Measure.RestartAttempted == nil || !*collection.Measure.RestartAttempted {
		t.Fatalf("restartAttempted = %v, want true", collection.Measure.RestartAttempted)
	}
	if collection.Measure.RestartSucceeded == nil || !*collection.Measure.RestartSucceeded {
		t.Fatalf("restartSucceeded = %v, want true", collection.Measure.RestartSucceeded)
	}
	if collection.Measure.ServiceState == nil || *collection.Measure.ServiceState != "Running" {
		t.Fatalf("serviceState = %v, want Running (estado FINAL)", collection.Measure.ServiceState)
	}
	if collection.Measure.Running == nil || !*collection.Measure.Running {
		t.Fatalf("running = %v, want true", collection.Measure.Running)
	}
}

func TestRestartStoppedServiceFails(t *testing.T) {
	fake := &fakeSensorService{state: "Stopped", keepStopped: true}
	cfg := newSensorCollectConfig(SensorTypeService, "fake-service", `{"restartIfStopped":true,"restartWaitMs":50}`, 10*time.Second)
	started := time.Now()
	collection := restartStoppedServiceWith(context.Background(), &cfg, "fake-service", fake)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("restart levou %v, want <= restartWaitMs", elapsed)
	}
	if collection.Measure.RestartAttempted == nil || !*collection.Measure.RestartAttempted {
		t.Fatalf("restartAttempted = %v, want true", collection.Measure.RestartAttempted)
	}
	if collection.Measure.RestartSucceeded == nil || *collection.Measure.RestartSucceeded {
		t.Fatalf("restartSucceeded = %v, want false", collection.Measure.RestartSucceeded)
	}
	if collection.Measure.ServiceState == nil || *collection.Measure.ServiceState != "Stopped" {
		t.Fatalf("serviceState = %v, want Stopped (estado FINAL)", collection.Measure.ServiceState)
	}
}

func TestCollectServiceSampleRestartFlag(t *testing.T) {
	// restartIfStopped=false NÃO deve chamar o controlador: sem serviço real, a
	// configuração ausente é o que garante o caminho "só reporta".
	cfg := newSensorCollectConfig(SensorTypeService, "svc-inexistente-xyz", "", 5*time.Second)
	collection := collectServiceSample(context.Background(), &cfg)
	if collection.Measure.RestartAttempted != nil {
		t.Fatalf("restartAttempted = %v, want nil sem a opcao", collection.Measure.RestartAttempted)
	}
	if collection.Measure.ServiceState == nil || (*collection.Measure.ServiceState != "NotFound" && *collection.Measure.ServiceState != "Stopped") {
		t.Fatalf("serviceState = %v, want NotFound/Stopped", collection.Measure.ServiceState)
	}
}

// ── decodeSensorJSON ──

func TestDecodeSensorJSONIgnoresNoise(t *testing.T) {
	type payload struct {
		Value int `json:"value"`
	}
	var parsed payload
	noisy := "WARNING: algo" + "\n" + "{\"value\":7}" + "\n" + "trailing"
	if err := decodeSensorJSON(noisy, &parsed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parsed.Value != 7 {
		t.Fatalf("value = %d, want 7", parsed.Value)
	}
	if err := decodeSensorJSON("nada", &parsed); err == nil {
		t.Fatal("texto sem JSON deveria virar erro")
	}
}
