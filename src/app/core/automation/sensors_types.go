package automation

import (
	"encoding/json"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Contrato do servidor para SENSORES (monitoramento periódico).
//
// Espelho de Discovery.Core/DTOs/SensorDtos.cs (parte agent-auth). O agente é
// PASSIVO na decisão: envia apenas EVIDÊNCIA (medidas cruas) e os canais
// numéricos já extraídos. Status, severidade e limiares são do SERVIDOR — o
// agente nunca recebe limiar nenhum.
//
// JSON em camelCase: o MVC do servidor serializa camelCase (Program.cs:
// JsonNamingPolicy.CamelCase). encoding/json do Go casa nomes
// case-insensitive no unmarshal, mas o marshal precisa do tag exato.
// ─────────────────────────────────────────────────────────────────────────────

// SensorType é o tipo do sensor no contrato (SensorType do API).
//
// O servidor serializa enums como STRING (JsonStringEnumConverter global).
// Aceitamos também o valor NUMÉRICO porque o mesmo payload pode chegar por um
// caminho interno (testes/harness): um único item com tipo numérico faria o
// unmarshal do slice inteiro falhar e o agente ficaria sem policy nenhuma.
type SensorType string

const (
	SensorTypeDiskSpace      SensorType = "DiskSpace"
	SensorTypeCpu            SensorType = "Cpu"
	SensorTypeMemory         SensorType = "Memory"
	SensorTypeUptime         SensorType = "Uptime"
	SensorTypeService        SensorType = "Service"
	SensorTypeProcess        SensorType = "Process"
	SensorTypePing           SensorType = "Ping"
	SensorTypeTcpPort        SensorType = "TcpPort"
	SensorTypeHTTPProbe      SensorType = "HttpProbe"
	SensorTypeHTTPThroughput SensorType = "HttpThroughput"
	SensorTypeDNSLatency     SensorType = "DnsLatency"
	SensorTypeBandwidth      SensorType = "Bandwidth"
	SensorTypeDiskIO         SensorType = "DiskIo"
	SensorTypeScript         SensorType = "Script"
	SensorTypeEventLogRate   SensorType = "EventLogRate"
	SensorTypeSmartStatus    SensorType = "SmartStatus"
	SensorTypeCertificate    SensorType = "CertificateExpiry"
	SensorTypeDefenderStatus SensorType = "DefenderStatus"
	SensorTypeBitLocker      SensorType = "BitLocker"
)

// sensorTypeByNumber espelha a ordem do enum SensorType do servidor.
var sensorTypeByNumber = map[int]SensorType{
	0:  SensorTypeDiskSpace,
	1:  SensorTypeCpu,
	2:  SensorTypeMemory,
	3:  SensorTypeUptime,
	4:  SensorTypeService,
	5:  SensorTypeProcess,
	6:  SensorTypePing,
	7:  SensorTypeTcpPort,
	8:  SensorTypeHTTPProbe,
	9:  SensorTypeHTTPThroughput,
	10: SensorTypeDNSLatency,
	11: SensorTypeBandwidth,
	12: SensorTypeDiskIO,
	13: SensorTypeScript,
	14: SensorTypeEventLogRate,
	15: SensorTypeSmartStatus,
	16: SensorTypeCertificate,
	17: SensorTypeDefenderStatus,
	18: SensorTypeBitLocker,
}

// UnmarshalJSON aceita o nome ("DiskSpace") e o valor numérico (0).
func (t *SensorType) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*t = ""
		return nil
	}
	if trimmed[0] == '"' {
		var name string
		if err := json.Unmarshal(data, &name); err != nil {
			return err
		}
		*t = SensorType(strings.TrimSpace(name))
		return nil
	}
	var numeric int
	if err := json.Unmarshal(data, &numeric); err != nil {
		return err
	}
	if mapped, ok := sensorTypeByNumber[numeric]; ok {
		*t = mapped
		return nil
	}
	// Tipo desconhecido (servidor mais novo): mantém vazio em vez de abortar o
	// sync; a coleta responde "tipo nao suportado" e o servidor decide.
	*t = ""
	return nil
}

// SensorChannel é um canal numérico declarado pelo sensor. O agente usa apenas
// a Key (filtro de canais extraídos de script) e IsPrimary (ordem de exibição).
type SensorChannel struct {
	Key       string `json:"key"`
	Label     string `json:"label,omitempty"`
	IsPrimary bool   `json:"isPrimary,omitempty"`
}

// SensorScriptRef é a referência (e, quando pedido no sync, o corpo) do script
// do catálogo usado por um sensor do tipo Script.
type SensorScriptRef struct {
	ScriptID          string               `json:"scriptId"`
	Name              string               `json:"name"`
	Version           string               `json:"version"`
	Summary           string               `json:"summary"`
	ScriptType        AutomationScriptType `json:"scriptType"`
	LastUpdatedAt     string               `json:"lastUpdatedAt"`
	ContentHashSHA256 string               `json:"contentHashSha256"`
	Content           string               `json:"content"`
}

// SensorPolicy é o AgentSensorPolicyDto: tudo o que o agente precisa para
// COLETAR. Sem limiares e sem configuração de alerta de propósito.
type SensorPolicy struct {
	SensorID        string           `json:"sensorId"`
	Key             string           `json:"key,omitempty"`
	Name            string           `json:"name"`
	SensorType      SensorType       `json:"sensorType"`
	Target          string           `json:"target,omitempty"`
	IntervalSeconds int              `json:"intervalSeconds"`
	TimeoutSeconds  int              `json:"timeoutSeconds"`
	OptionsJSON     string           `json:"optionsJson,omitempty"`
	Channels        []SensorChannel  `json:"channels"`
	ExtractorsJSON  string           `json:"extractorsJson,omitempty"`
	ExitCodeMapJSON string           `json:"exitCodeMapJson,omitempty"`
	Script          *SensorScriptRef `json:"script,omitempty"`
}

// SensorSyncRequest é o corpo do POST /agent-auth/me/sensors/sync.
type SensorSyncRequest struct {
	KnownSensorFingerprint string `json:"knownSensorFingerprint,omitempty"`
	// IncludeScriptContent: true só no sync FORÇADO (ou na recuperação de um
	// sensor de script sem corpo no cache). No sync periódico fica false para o
	// servidor poder responder "up to date" sem payload.
	IncludeScriptContent bool `json:"includeScriptContent"`
}

// SensorSyncResponse é o AgentSensorSyncResponse.
type SensorSyncResponse struct {
	UpToDate               bool           `json:"upToDate"`
	SensorFingerprint      string         `json:"sensorFingerprint"`
	GeneratedAt            string         `json:"generatedAt"`
	SensorCount            int            `json:"sensorCount"`
	DefaultIntervalSeconds int            `json:"defaultIntervalSeconds"`
	Sensors                []SensorPolicy `json:"sensors"`
}

// SensorMeasure é o AgentSensorMeasureDto: evidência CRUA. Todos os campos são
// ponteiros/nullable no contrato — nil significa "o tipo não produz esse dado"
// (o servidor deriva canais apenas do que existe).
type SensorMeasure struct {
	// Disco
	FreePercent *float64 `json:"freePercent,omitempty"`
	UsedPercent *float64 `json:"usedPercent,omitempty"`
	TotalBytes  *float64 `json:"totalBytes,omitempty"`
	FreeBytes   *float64 `json:"freeBytes,omitempty"`

	// Serviço / processo
	ServiceState    *string  `json:"serviceState,omitempty"`
	ProcessState    *string  `json:"processState,omitempty"`
	Running         *bool    `json:"running,omitempty"`
	ProcessCount    *int     `json:"processCount,omitempty"`
	WorkingSetBytes *float64 `json:"workingSetBytes,omitempty"`

	// Ping
	Replies      *int     `json:"replies,omitempty"`
	Sent         *int     `json:"sent,omitempty"`
	AvgRTTMs     *float64 `json:"avgRttMs,omitempty"`
	LatencyMinMs *float64 `json:"latencyMinMs,omitempty"`
	LatencyMaxMs *float64 `json:"latencyMaxMs,omitempty"`
	JitterMs     *float64 `json:"jitterMs,omitempty"`
	LossPercent  *float64 `json:"lossPercent,omitempty"`

	// HTTP / TCP / DNS
	StatusCode *int     `json:"statusCode,omitempty"`
	DNSMs      *float64 `json:"dnsMs,omitempty"`
	ConnectMs  *float64 `json:"connectMs,omitempty"`
	TLSMs      *float64 `json:"tlsMs,omitempty"`
	TTFBMs     *float64 `json:"ttfbMs,omitempty"`
	TotalMs    *float64 `json:"totalMs,omitempty"`
	Bytes      *float64 `json:"bytes,omitempty"`

	// Throughput / uptime / banda / disco I/O
	DownloadMbps  *float64 `json:"downloadMbps,omitempty"`
	UploadMbps    *float64 `json:"uploadMbps,omitempty"`
	UptimeSeconds *float64 `json:"uptimeSeconds,omitempty"`
	ResolveMs     *float64 `json:"resolveMs,omitempty"`
	RxBps         *float64 `json:"rxBps,omitempty"`
	TxBps         *float64 `json:"txBps,omitempty"`
	ReadBps       *float64 `json:"readBps,omitempty"`
	WriteBps      *float64 `json:"writeBps,omitempty"`
	BusyPercent   *float64 `json:"busyPercent,omitempty"`
	DiskLatencyMs *float64 `json:"diskLatencyMs,omitempty"`

	// Log de eventos (EventLogRate)
	EventCount         *int `json:"eventCount,omitempty"`
	EventWindowMinutes *int `json:"eventWindowMinutes,omitempty"`
	// LastEventAt é DateTime no contrato: viaja como RFC3339 (string).
	LastEventAt *string `json:"lastEventAt,omitempty"`

	// SMART (SmartStatus)
	SmartStatus        *string  `json:"smartStatus,omitempty"`
	TemperatureC       *float64 `json:"temperatureC,omitempty"`
	PowerOnHours       *int     `json:"powerOnHours,omitempty"`
	ReallocatedSectors *int     `json:"reallocatedSectors,omitempty"`

	// Certificado TLS (CertificateExpiry)
	DaysToExpiry       *float64 `json:"daysToExpiry,omitempty"`
	ExpiresAt          *string  `json:"expiresAt,omitempty"`
	CertificateSubject *string  `json:"certificateSubject,omitempty"`

	// Defender (DefenderStatus)
	DefenderEnabled         *bool    `json:"defenderEnabled,omitempty"`
	DefenderRealTimeEnabled *bool    `json:"defenderRealTimeEnabled,omitempty"`
	SignatureAgeDays        *float64 `json:"signatureAgeDays,omitempty"`
	SignatureVersion        *string  `json:"signatureVersion,omitempty"`

	// BitLocker (BitLocker)
	BitLockerStatus            *string  `json:"bitLockerStatus,omitempty"`
	BitLockerEncryptionPercent *float64 `json:"bitLockerEncryptionPercent,omitempty"`

	// Serviço: reinício automático (restartIfStopped)
	RestartAttempted *bool `json:"restartAttempted,omitempty"`
	RestartSucceeded *bool `json:"restartSucceeded,omitempty"`

	// Script
	ExitCode *int    `json:"exitCode,omitempty"`
	TimedOut bool    `json:"timedOut"`
	Stdout   *string `json:"stdout,omitempty"`
	Stderr   *string `json:"stderr,omitempty"`
}

// SensorResult é o AgentSensorResultDto: uma amostra de UM alvo do sensor.
type SensorResult struct {
	SensorID   string        `json:"sensorId"`
	TargetKey  string        `json:"targetKey"`
	RunID      string        `json:"runId"`
	StartedAt  string        `json:"startedAt,omitempty"`
	FinishedAt string        `json:"finishedAt,omitempty"`
	DurationMs *int          `json:"durationMs,omitempty"`
	Measure    SensorMeasure `json:"measure"`
	// Channels: canais já extraídos no agente (MDZ_METRIC/extratores). Para
	// sensores built-in é opcional — o servidor deriva de Measure.
	Channels map[string]float64 `json:"channels,omitempty"`
	// Error é ERRO DE COLETA: o servidor marca Unknown + evento RunError.
	Error string `json:"error,omitempty"`
	// DeclaredStatus/DeclaredMessage são o que o PRÓPRIO script declarou no
	// stdout (MDZ_STATUS/MDZ_MESSAGE). O servidor valida e traduz; o agente não
	// decide status — só transporta a declaração junto da evidência.
	DeclaredStatus  string `json:"declaredStatus,omitempty"`
	DeclaredMessage string `json:"declaredMessage,omitempty"`
}

// SensorResultBatch é o AgentSensorResultBatch (corpo de /sensors/samples).
type SensorResultBatch struct {
	AgentID       string         `json:"agentId"`
	CorrelationID string         `json:"correlationId,omitempty"`
	Samples       []SensorResult `json:"samples"`
}

// SensorRejected é um item rejeitado pelo servidor na ingestão.
type SensorRejected struct {
	SensorID  string `json:"sensorId"`
	TargetKey string `json:"targetKey"`
	Reason    string `json:"reason"`
}

// SensorResultResponse é o AgentSensorResultResponse.
type SensorResultResponse struct {
	Accepted int              `json:"accepted"`
	Rejected []SensorRejected `json:"rejected"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Estado LOCAL (nunca vai para o servidor)
// ─────────────────────────────────────────────────────────────────────────────

// PersistedSensorPolicy é o snapshot local da policy de sensores, para o agente
// continuar monitorando logo após um restart (antes do primeiro sync).
type PersistedSensorPolicy struct {
	Fingerprint            string         `json:"fingerprint"`
	DefaultIntervalSeconds int            `json:"defaultIntervalSeconds"`
	Sensors                []SensorPolicy `json:"sensors"`
	SavedAt                string         `json:"savedAt"`
}

// SensorConfig é o estado local de UM sensor: a policy do servidor já
// normalizada + o que o agendamento precisa. Não é enviado ao servidor.
type SensorConfig struct {
	Policy SensorPolicy
	// Interval efetivo (default quando a policy não define ou define < mínimo).
	Interval time.Duration
	// Timeout efetivo da coleta (teto de 300s).
	Timeout time.Duration
	// Jitter determinístico (0..29s) por sensorId: evita que o parque inteiro
	// bata no servidor no mesmo instante — o valor não muda entre restarts.
	Jitter time.Duration
	// Options/Extractors/DeclaredChannels são o OptionsJson/ExtractorsJson/
	// Channels já parseados (parse uma vez por sync, não por coleta).
	Options          sensorOptions
	Extractors       []sensorExtractor
	DeclaredChannels map[string]bool
}

// sensorCollection é o produto bruto de UMA coleta para UM alvo. O runner
// transforma em SensorResult (adiciona runId/timestamps) antes de enfileirar.
type sensorCollection struct {
	TargetKey string
	Measure   SensorMeasure
	Channels  map[string]float64
	// Err é erro de coleta (servidor marca Unknown + RunError). Diferente de
	// uma leitura "ruim": para um volume inexistente, por exemplo, devolvemos
	// medida vazia (o servidor decide Failing), não Err.
	Err string
	// DeclaredStatus/DeclaredMessage vêm do protocolo MDZ do script.
	DeclaredStatus  string
	DeclaredMessage string
}

// ── Helpers de ponteiro (nil = dado ausente no contrato) ──

func sensorFloat(v float64) *float64 { return &v }
func sensorInt(v int) *int           { return &v }
func sensorBool(v bool) *bool        { return &v }
func sensorString(v string) *string  { return &v }
