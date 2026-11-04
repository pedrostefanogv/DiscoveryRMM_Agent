package automation

import (
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// DIAGNÓSTICO DOS SENSORES (painel de depuração).
//
// Este arquivo é SOMENTE LEITURA sobre o estado do motor: monta um retrato do
// agendamento (próximo disparo, atraso, último resultado) e do estado da fila
// (em voo, pendentes, backoff) para o operador responder "por que este sensor
// não coletou?" sem abrir o log do agente.
//
// Regras de concorrência:
//   - sensorMu é segurado APENAS para copiar o estado (slices/mapas escalares).
//     A montagem das views roda FORA do lock: um agendamento concorrente não
//     pode ficar preso atrás da serialização de diagnóstico;
//   - o histórico tem mutex próprio (sensorHistoryMu) e NUNCA entrega o slice
//     interno — todo acesso externo passa por cópia defensiva.
//
// Nada aqui decide severidade, limiar ou alerta: isso é do servidor
// (SensorEvaluator no API), igual ao resto do motor.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// sensorDebugHistoryMax é o teto do ring de histórico de amostras. Um sensor
	// de disco com 6 volumes gera 6 amostras por ciclo; 100 cobre com folga o
	// que o painel exibe sem crescer sem limite em agentes de longa duração.
	sensorDebugHistoryMax = 100
	// sensorDebugLastResultsMax é quantas amostras POR SENSOR a view devolve.
	sensorDebugLastResultsMax = 10
)

// sensorHistoryEntry é o que fica guardado no ring: a amostra exibida MAIS o
// sensor dono. A view não repete o SensorID (o RunID é um uuid opaco), então a
// associação sensor↔amostra precisa ser preservada aqui dentro.
type sensorHistoryEntry struct {
	SensorID string
	View     SensorRunView
}

// SensorRunView é UMA amostra coletada, no formato que o painel consome.
// Espelha o subconjunto útil de SensorResult (o payload de sync continua sendo
// SensorResult — este tipo é só para exibição).
type SensorRunView struct {
	RunID           string             `json:"runId,omitempty"`
	TargetKey       string             `json:"targetKey,omitempty"`
	StartedAt       string             `json:"startedAt,omitempty"`
	FinishedAt      string             `json:"finishedAt,omitempty"`
	DurationMs      *int               `json:"durationMs,omitempty"`
	Error           string             `json:"error,omitempty"`
	DeclaredStatus  string             `json:"declaredStatus,omitempty"`
	DeclaredMessage string             `json:"declaredMessage,omitempty"`
	Channels        map[string]float64 `json:"channels,omitempty"`
	Measure         SensorMeasure      `json:"measure"`
}

// SensorDebugView é o retrato de UM sensor: configuração efetiva, estado do
// script, agendamento e as últimas amostras.
type SensorDebugView struct {
	SensorID        string          `json:"sensorId"`
	Key             string          `json:"key,omitempty"`
	Name            string          `json:"name,omitempty"`
	SensorType      SensorType      `json:"sensorType,omitempty"`
	Target          string          `json:"target,omitempty"`
	IntervalSeconds int             `json:"intervalSeconds"`
	TimeoutSeconds  int             `json:"timeoutSeconds"`
	JitterSeconds   int             `json:"jitterSeconds"`
	Channels        []SensorChannel `json:"channels,omitempty"`
	ChannelCount    int             `json:"channelCount"`
	ScriptCached    bool            `json:"scriptCached"`
	ScriptName      string          `json:"scriptName,omitempty"`
	ScriptVersion   string          `json:"scriptVersion,omitempty"`
	// LastRunAt é o último DISPARO do sensor (chave de agenda, alvo vazio),
	// não o último resultado de um alvo específico. Nil quando nunca rodou.
	LastRunAt *string `json:"lastRunAt,omitempty"`
	// NextRunAt é nil quando o sensor nunca rodou (devido agora).
	NextRunAt *string `json:"nextRunAt,omitempty"`
	Overdue   bool    `json:"overdue"`
	// LastResults traz as amostras deste sensor, MAIS RECENTES PRIMEIRO.
	LastResults []SensorRunView `json:"lastResults,omitempty"`
}

// SensorEngineStatus é o estado GLOBAL do motor (fila, backoff, concorrência).
type SensorEngineStatus struct {
	Fingerprint      string  `json:"fingerprint,omitempty"`
	FingerprintShort string  `json:"fingerprintShort,omitempty"`
	LastSyncAt       *string `json:"lastSyncAt,omitempty"`
	BackoffUntil     *string `json:"backoffUntil,omitempty"`
	BackoffActive    bool    `json:"backoffActive"`
	Disabled         bool    `json:"disabled"`
	PolicyLoaded     bool    `json:"policyLoaded"`
	// InFlight é quantas coletas estão em voo; MaxConcurrency é o teto do
	// semáforo (sensorMaxConcurrency).
	InFlight               int    `json:"inFlight"`
	MaxConcurrency         int    `json:"maxConcurrency"`
	PendingSamples         int    `json:"pendingSamples"`
	ScheduleTickSeconds    int    `json:"scheduleTickSeconds"`
	FlushIntervalSeconds   int    `json:"flushIntervalSeconds"`
	DefaultIntervalSeconds int    `json:"defaultIntervalSeconds"`
	LastError              string `json:"lastError,omitempty"`
}

// SensorDebugSnapshot é o retrato completo devolvido à camada App.
type SensorDebugSnapshot struct {
	// AgentID fica VAZIO quando s.getConfig é nil ou o RuntimeConfig não traz
	// AgentID (o núcleo não inventa identidade).
	AgentID string `json:"agentId,omitempty"`
	// GeneratedAt é o instante UTC (RFC3339) de montagem do retrato.
	GeneratedAt string `json:"generatedAt"`
	// DebugMode é sempre false no núcleo: o painel é habilitado/desabilitado na
	// camada App (a decisão depende de config de UI, não do motor). O campo
	// existe para o contrato não mudar quando essa camada passar a preenchê-lo.
	DebugMode bool               `json:"debugMode"`
	Engine    SensorEngineStatus `json:"engine"`
	// Sensors é sempre não-nil (slice vazio quando não há policy), para o
	// consumidor não precisar tratar null.
	Sensors       []SensorDebugView `json:"sensors"`
	HistoryWindow int               `json:"historyWindow"`
}

// recordSensorHistory converte as amostras recém-coletadas e as anexa ao ring,
// descartando as mais antigas acima do teto (FIFO). Chamado pelo runner antes
// de enfileirar para entrega — o histórico é local e independente da outbox.
func (s *Service) recordSensorHistory(results []SensorResult) {
	if s == nil || len(results) == 0 {
		return
	}
	entries := make([]sensorHistoryEntry, 0, len(results))
	for _, result := range results {
		entries = append(entries, sensorHistoryEntry{
			SensorID: strings.TrimSpace(result.SensorID),
			View:     sensorRunViewFromResult(result),
		})
	}

	s.sensorHistoryMu.Lock()
	defer s.sensorHistoryMu.Unlock()
	s.sensorHistory = append(s.sensorHistory, entries...)
	if len(s.sensorHistory) > sensorDebugHistoryMax {
		// Descarta a cabeça (mais antigas) preservando a cauda. O append em
		// slice novo evita segurar memória das amostras já descartadas.
		s.sensorHistory = append([]sensorHistoryEntry(nil), s.sensorHistory[len(s.sensorHistory)-sensorDebugHistoryMax:]...)
	}
}

// sensorHistorySnapshot devolve CÓPIA do ring (ordem cronológica). Nunca
// devolve o slice interno: o chamador pode ordenar/filtrar à vontade.
func (s *Service) sensorHistorySnapshot() []sensorHistoryEntry {
	if s == nil {
		return nil
	}
	s.sensorHistoryMu.Lock()
	defer s.sensorHistoryMu.Unlock()
	if len(s.sensorHistory) == 0 {
		return nil
	}
	out := make([]sensorHistoryEntry, len(s.sensorHistory))
	copy(out, s.sensorHistory)
	return out
}

// sensorRunViewFromResult converte uma amostra coletada para a view do painel.
func sensorRunViewFromResult(result SensorResult) SensorRunView {
	return SensorRunView{
		RunID:           result.RunID,
		TargetKey:       result.TargetKey,
		StartedAt:       result.StartedAt,
		FinishedAt:      result.FinishedAt,
		DurationMs:      result.DurationMs,
		Error:           result.Error,
		DeclaredStatus:  result.DeclaredStatus,
		DeclaredMessage: result.DeclaredMessage,
		Channels:        result.Channels,
		Measure:         result.Measure,
	}
}

// sensorDebugTimePointer formata um instante como RFC3339 UTC. Zero vira nil:
// "nunca aconteceu" precisa ser distinguível de "aconteceu em 0001-01-01".
func sensorDebugTimePointer(at time.Time) *string {
	if at.IsZero() {
		return nil
	}
	formatted := at.UTC().Format(time.RFC3339)
	return &formatted
}

// sensorFingerprintShort encurta o fingerprint para exibição, marcando o corte.
func sensorFingerprintShort(fingerprint string) string {
	const max = 12
	if len(fingerprint) <= max {
		return fingerprint
	}
	return fingerprint[:max] + "…"
}

// SensorDebugSnapshot monta o retrato do motor de sensores.
//
// Segura sensorMu UMA vez para copiar o estado e monta as views depois, fora do
// lock. Nunca entra em pânico com Service nulo, mapas nulos ou histórico vazio.
func (s *Service) SensorDebugSnapshot() SensorDebugSnapshot {
	snapshot := SensorDebugSnapshot{
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		DebugMode:     false,
		HistoryWindow: sensorDebugHistoryMax,
		Sensors:       []SensorDebugView{},
	}
	if s == nil {
		return snapshot
	}

	// ── Cópia única sob lock ──
	s.sensorMu.Lock()
	sensors := make([]SensorConfig, len(s.sensors))
	copy(sensors, s.sensors)
	lastRun := make(map[string]time.Time, len(s.sensorLastRun))
	for key, value := range s.sensorLastRun {
		lastRun[key] = value
	}
	fingerprint := s.sensorFingerprint
	disabled := s.sensorDisabled
	backoffUntil := s.sensorSyncBackoffUntil
	lastSyncAt := s.sensorLastSyncAt
	policyLoaded := s.sensorPolicyLoaded
	pendingSamples := len(s.sensorPending)
	s.sensorMu.Unlock()

	// len/cap do semáforo são atômicos por natureza: leitura fora do lock é
	// segura (o canal não é reatribuído após a construção).
	inFlight := 0
	maxConcurrency := 0
	if s.sensorSem != nil {
		inFlight = len(s.sensorSem)
		maxConcurrency = cap(s.sensorSem)
	}

	history := s.sensorHistorySnapshot()
	now := time.Now()

	// ── Montagem das views (fora do sensorMu) ──
	views := make([]SensorDebugView, 0, len(sensors))
	for _, cfg := range sensors {
		policy := cfg.Policy
		sensorID := strings.TrimSpace(policy.SensorID)

		// Agendamento: lastRun da chave de AGENDA (alvo vazio). A chave por alvo
		// registra o último resultado do alvo, não o disparo do sensor.
		last := lastRun[sensorRunKey(sensorID, "")]
		next := sensorNextRun(last, cfg.Interval, cfg.Jitter)
		overdue := sensorDue(now, last, cfg.Interval, cfg.Jitter)

		view := SensorDebugView{
			SensorID:        sensorID,
			Key:             policy.Key,
			Name:            policy.Name,
			SensorType:      policy.SensorType,
			Target:          policy.Target,
			IntervalSeconds: int(cfg.Interval / time.Second),
			TimeoutSeconds:  int(cfg.Timeout / time.Second),
			JitterSeconds:   int(cfg.Jitter / time.Second),
			Channels:        policy.Channels,
			ChannelCount:    len(policy.Channels),
			Overdue:         overdue,
			LastRunAt:       sensorDebugTimePointer(last),
			NextRunAt:       sensorDebugTimePointer(next),
			LastResults:     sensorDebugLastResults(history, sensorID),
		}
		if policy.Script != nil {
			view.ScriptName = policy.Script.Name
			view.ScriptVersion = policy.Script.Version
			view.ScriptCached = strings.TrimSpace(policy.Script.Content) != ""
		}
		views = append(views, view)
	}

	agentID := ""
	if s.getConfig != nil {
		agentID = strings.TrimSpace(s.getConfig().AgentID)
	}

	snapshot.AgentID = agentID
	snapshot.Sensors = views
	snapshot.Engine = SensorEngineStatus{
		Fingerprint:            fingerprint,
		FingerprintShort:       sensorFingerprintShort(fingerprint),
		LastSyncAt:             sensorDebugTimePointer(lastSyncAt),
		BackoffUntil:           sensorDebugTimePointer(backoffUntil),
		BackoffActive:          !backoffUntil.IsZero() && now.Before(backoffUntil),
		Disabled:               disabled,
		PolicyLoaded:           policyLoaded,
		InFlight:               inFlight,
		MaxConcurrency:         maxConcurrency,
		PendingSamples:         pendingSamples,
		ScheduleTickSeconds:    int(sensorScheduleTick / time.Second),
		FlushIntervalSeconds:   int(sensorFlushInterval / time.Second),
		DefaultIntervalSeconds: int(sensorDefaultInterval / time.Second),
	}
	return snapshot
}

// sensorDebugLastResults filtra o histórico pelo sensor e devolve no máximo
// sensorDebugLastResultsMax amostras, MAIS RECENTES PRIMEIRO.
func sensorDebugLastResults(history []sensorHistoryEntry, sensorID string) []SensorRunView {
	// O histórico é cronológico (mais antigo primeiro): percorre de trás para
	// frente para parar assim que juntar o teto por sensor.
	out := make([]SensorRunView, 0, sensorDebugLastResultsMax)
	for i := len(history) - 1; i >= 0 && len(out) < sensorDebugLastResultsMax; i-- {
		if history[i].SensorID != sensorID {
			continue
		}
		out = append(out, history[i].View)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
