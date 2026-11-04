package automation

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// MOTOR DOS SENSORES (monitoramento periódico).
//
// Responsabilidade do agente: COLETAR evidência no ritmo do sensor e ENTREGAR ao
// servidor. Status, severidade, limiares e alertas são decididos no servidor
// (SensorEvaluator no API) — aqui não existe limiar nenhum.
//
// Estrutura:
//   - runSensorsLoop: ticker de 15s para agenda + ticker de 10s para flush;
//   - agendamento por sensor: due = lastRun + intervalSeconds + jitter;
//   - coletas concorrentes limitadas por semáforo (4): coleta pode abrir
//     processo (script/ping) e não queremos 20 PowerShell em paralelo numa
//     máquina fraca;
//   - resultados vão para um buffer e são entregues em lotes de até 100 a cada
//     10s (ou na hora, quando o buffer enche);
//   - entrega reusa a OUTBOX durável da automação (automation_callback_queue)
//     via sendOrQueueCallback, com CallbackTypeSensorSamples.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// sensorScheduleTick é o pulso da agenda. 15s é o menor intervalo aceito
	// pelo servidor (SensorDefaults.MinIntervalSeconds = 30), com folga.
	sensorScheduleTick = 15 * time.Second
	// sensorFlushInterval: amostras acumuladas não esperam o próximo ciclo de
	// coleta para subir.
	sensorFlushInterval = 10 * time.Second
	// sensorResultBatchSize espelha SensorDefaults.MaxBatchSamples do servidor.
	sensorResultBatchSize = 100
	// sensorMaxConcurrency limita coletas simultâneas.
	sensorMaxConcurrency = 4
	// sensorMaxTimeout é o teto por coleta (timeoutSeconds acima disso é
	// ignorado): sensor travado não pode segurar o semáforo indefinidamente.
	sensorMaxTimeout = 300 * time.Second
	// sensorDefaultInterval espelha SensorDefaults.DefaultIntervalSeconds (5 min).
	sensorDefaultInterval = 300 * time.Second
	// sensorMinInterval espelha SensorDefaults.MinIntervalSeconds.
	sensorMinInterval = 30 * time.Second
	// sensorJitterMax é a janela do jitter determinístico por sensor.
	sensorJitterMax = 30 * time.Second
	// sensorSyncMinInterval evita que o refreshPolicy (a cada ~5 min) e a UI
	// disparem syncs de sensores em rajada.
	sensorSyncMinInterval = 60 * time.Second
	// sensorSyncBackoffError é o retry após falha transitória de sync.
	sensorSyncBackoffError = 2 * time.Minute
	// sensorSyncBackoffNotFound é o retry quando o servidor não tem o endpoint
	// (backend anterior ao recurso) — backoff longo, log único.
	sensorSyncBackoffNotFound = 15 * time.Minute
)

// ── Agendamento ──

// sensorJitterFor devolve o jitter determinístico (0..29s) de um sensor.
// Determinístico de propósito: o mesmo sensorId gera sempre o mesmo desvio,
// então os sensores de um parque se espalham pelo ciclo em vez de formarem
// uma rajada sincronizada no servidor.
func sensorJitterFor(sensorID string) time.Duration {
	trimmed := strings.TrimSpace(strings.ToLower(sensorID))
	if trimmed == "" {
		return 0
	}
	sum := sha256.Sum256([]byte(trimmed))
	return time.Duration(binary.BigEndian.Uint64(sum[:8])%uint64(sensorJitterMax/time.Second)) * time.Second
}

// sensorDue informa se o sensor deve coletar agora.
// lastRun zero = sensor novo/pendente: roda já no primeiro ciclo.
func sensorDue(now, lastRun time.Time, interval, jitter time.Duration) bool {
	if lastRun.IsZero() {
		return true
	}
	if interval <= 0 {
		interval = sensorDefaultInterval
	}
	return !now.Before(lastRun.Add(interval + jitter))
}

// sensorNextRun devolve o instante em que o sensor fica devido.
func sensorNextRun(lastRun time.Time, interval, jitter time.Duration) time.Time {
	if lastRun.IsZero() {
		return lastRun
	}
	if interval <= 0 {
		interval = sensorDefaultInterval
	}
	return lastRun.Add(interval + jitter)
}

// buildSensorConfig normaliza a policy recebida (intervalo/timeout/opções).
func buildSensorConfig(policy SensorPolicy, defaultInterval time.Duration) SensorConfig {
	if defaultInterval <= 0 {
		defaultInterval = sensorDefaultInterval
	}
	interval := time.Duration(policy.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = defaultInterval
	}
	if interval < sensorMinInterval {
		interval = sensorMinInterval
	}
	timeout := time.Duration(policy.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > sensorMaxTimeout {
		timeout = sensorMaxTimeout
	}
	declared := make(map[string]bool, len(policy.Channels))
	for _, channel := range policy.Channels {
		key := strings.ToLower(strings.TrimSpace(channel.Key))
		if key != "" {
			declared[key] = true
		}
	}
	return SensorConfig{
		Policy:           policy,
		Interval:         interval,
		Timeout:          timeout,
		Jitter:           sensorJitterFor(policy.SensorID),
		Options:          parseSensorOptions(policy.OptionsJSON),
		Extractors:       parseSensorExtractors(policy.ExtractorsJSON),
		DeclaredChannels: declared,
	}
}

// ── Marcadores / chaves do último disparo ──

// sensorLastRunMarkerKey é a chave durável do último disparo.
// Formato exigido pela arquitetura: sensor:lastrun:<sensorId>:<targetKey>.
func sensorLastRunMarkerKey(sensorID, targetKey string) string {
	return "sensor:lastrun:" + strings.TrimSpace(sensorID) + ":" + strings.TrimSpace(targetKey)
}

// sensorRunKey é a chave em memória (sensorID|targetKey). GUID não contém "|".
func sensorRunKey(sensorID, targetKey string) string {
	return strings.TrimSpace(sensorID) + "|" + strings.TrimSpace(targetKey)
}

// splitSensorRunKey desfaz sensorRunKey.
func splitSensorRunKey(key string) (string, string) {
	parts := strings.SplitN(key, "|", 2)
	if len(parts) == 1 {
		return strings.TrimSpace(parts[0]), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

// sensorMarkerKeys lista TODAS as chaves de marcador de sensores que devem
// sobreviver ao cleanupOrphanedMarkers (chamado pelo collectValidMarkerKeys).
func (s *Service) sensorMarkerKeys() []string {
	s.sensorMu.Lock()
	defer s.sensorMu.Unlock()
	keys := make([]string, 0, len(s.sensors)+len(s.sensorLastRun))
	for _, cfg := range s.sensors {
		sensorID := strings.TrimSpace(cfg.Policy.SensorID)
		if sensorID == "" {
			continue
		}
		keys = append(keys, sensorLastRunMarkerKey(sensorID, ""))
	}
	for key := range s.sensorLastRun {
		sensorID, targetKey := splitSensorRunKey(key)
		if sensorID == "" {
			continue
		}
		keys = append(keys, sensorLastRunMarkerKey(sensorID, targetKey))
	}
	return keys
}

// persistSensorMarker grava o marcador do último disparo (best-effort).
func (s *Service) persistSensorMarker(sensorID, targetKey string, at time.Time) {
	sensorID = strings.TrimSpace(sensorID)
	if sensorID == "" || s.db == nil || s.getConfig == nil {
		return
	}
	agentID := strings.TrimSpace(s.getConfig().AgentID)
	if agentID == "" {
		return
	}
	value := at.UTC().Format(time.RFC3339Nano)
	if err := s.db.SetAutomationMarker(agentID, sensorLastRunMarkerKey(sensorID, targetKey), value); err != nil {
		s.logf("sensores: falha ao gravar marcador de ultimo disparo (sensor=%s alvo=%q): %v", sensorID, targetKey, err)
	}
}

// ── Estado em memória ──

// sensorStateSnapshot copia a policy atual e os últimos disparos (o loop não
// segura o mutex durante a coleta).
func (s *Service) sensorStateSnapshot() ([]SensorConfig, map[string]time.Time, bool) {
	s.sensorMu.Lock()
	defer s.sensorMu.Unlock()
	sensors := make([]SensorConfig, len(s.sensors))
	copy(sensors, s.sensors)
	lastRun := make(map[string]time.Time, len(s.sensorLastRun))
	for key, value := range s.sensorLastRun {
		lastRun[key] = value
	}
	return sensors, lastRun, s.sensorDisabled
}

// markSensorDispatched registra o disparo (memória + marcador durável) ANTES da
// coleta: sem isso o ticker de 15s redispararia o mesmo sensor enquanto uma
// coleta longa (timeout de até 300s) ainda está em voo.
func (s *Service) markSensorDispatched(cfg SensorConfig, at time.Time) {
	sensorID := strings.TrimSpace(cfg.Policy.SensorID)
	if sensorID == "" {
		return
	}
	s.sensorMu.Lock()
	if s.sensorLastRun == nil {
		s.sensorLastRun = make(map[string]time.Time)
	}
	s.sensorLastRun[sensorRunKey(sensorID, "")] = at
	s.sensorMu.Unlock()
	s.persistSensorMarker(sensorID, "", at)
}

// recordSensorTargetRun registra o disparo de um ALVO específico (ex.: volume C:).
func (s *Service) recordSensorTargetRun(sensorID, targetKey string, at time.Time) {
	sensorID = strings.TrimSpace(sensorID)
	targetKey = strings.TrimSpace(targetKey)
	if sensorID == "" || targetKey == "" {
		return
	}
	s.sensorMu.Lock()
	if s.sensorLastRun == nil {
		s.sensorLastRun = make(map[string]time.Time)
	}
	s.sensorLastRun[sensorRunKey(sensorID, targetKey)] = at
	s.sensorMu.Unlock()
	s.persistSensorMarker(sensorID, targetKey, at)
}

// signalSensorWake acorda o loop para o run-now (sem bloquear).
func (s *Service) signalSensorWake() {
	if s == nil || s.sensorWake == nil {
		return
	}
	select {
	case s.sensorWake <- struct{}{}:
	default:
	}
}

// markSensorsDue marca os sensores para coleta imediata. ids vazio = todos.
// Devolve quantos sensores foram marcados.
func (s *Service) markSensorsDue(ids []string) int {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			wanted[trimmed] = true
		}
	}

	s.sensorMu.Lock()
	defer s.sensorMu.Unlock()
	if s.sensorLastRun == nil {
		s.sensorLastRun = make(map[string]time.Time)
	}
	count := 0
	for _, cfg := range s.sensors {
		sensorID := strings.TrimSpace(cfg.Policy.SensorID)
		if sensorID == "" {
			continue
		}
		if len(wanted) > 0 && !wanted[sensorID] {
			continue
		}
		// Zero = devido já (sensorDue trata lastRun zero como "novo").
		s.sensorLastRun[sensorRunKey(sensorID, "")] = time.Time{}
		count++
	}
	return count
}

// ── Policy (sync) ──

// ensurePersistedSensorsLoaded carrega o snapshot local da policy de sensores,
// para o agente retomar o monitoramento logo após um restart — antes do primeiro
// sync com o servidor.
func (s *Service) ensurePersistedSensorsLoaded() {
	s.sensorMu.Lock()
	if s.sensorPolicyLoaded {
		s.sensorMu.Unlock()
		return
	}
	s.sensorPolicyLoaded = true
	s.sensorMu.Unlock()

	if s.db == nil || s.getConfig == nil {
		return
	}
	agentID := strings.TrimSpace(s.getConfig().AgentID)
	if agentID == "" {
		return
	}
	payload, _, _, found, err := s.db.GetSensorConfig(agentID)
	if err != nil || !found {
		return
	}
	var persisted PersistedSensorPolicy
	if err := json.Unmarshal(payload, &persisted); err != nil || len(persisted.Sensors) == 0 {
		return
	}
	s.applySensorPolicy(agentID, &SensorSyncResponse{
		SensorFingerprint:      persisted.Fingerprint,
		DefaultIntervalSeconds: persisted.DefaultIntervalSeconds,
		Sensors:                persisted.Sensors,
	}, false)
	s.logf("sensores: policy local carregada do cache (%d sensores)", len(persisted.Sensors))
}

// refreshSensors sincroniza a policy de sensores com o servidor.
//
// Best-effort por contrato: erro aqui NUNCA derruba o policy-sync da automação
// (são recursos independentes, com fingerprint próprio).
func (s *Service) refreshSensors(ctx context.Context, force bool) {
	if s == nil || s.getConfig == nil || s.client == nil {
		return
	}
	cfg := s.getConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Token) == "" {
		return
	}

	now := time.Now()
	s.sensorMu.Lock()
	if !force {
		if !s.sensorSyncBackoffUntil.IsZero() && now.Before(s.sensorSyncBackoffUntil) {
			s.sensorMu.Unlock()
			return
		}
		if !s.sensorLastSyncAt.IsZero() && now.Sub(s.sensorLastSyncAt) < sensorSyncMinInterval {
			s.sensorMu.Unlock()
			return
		}
	}
	// Reserva a janela ANTES do HTTP: dois refreshPolicy concorrentes não
	// disparam dois syncs.
	s.sensorLastSyncAt = now
	knownFingerprint := strings.TrimSpace(s.sensorFingerprint)
	s.sensorMu.Unlock()

	// Conteúdo de script só é pedido no sync FORÇADO (ou na recuperação abaixo):
	// no sync periódico o fingerprint basta, e o servidor responde "up to date"
	// sem payload (é o atalho que evita reenviar o corpo dos scripts a cada 5
	// min para toda a frota).
	request := SensorSyncRequest{IncludeScriptContent: force}
	if knownFingerprint != "" {
		request.KnownSensorFingerprint = knownFingerprint
	}
	correlationID := uuid.NewString()

	response, err := s.client.SyncSensors(ctx, cfg, request, correlationID)
	if err != nil {
		// Contexto cancelado (ex.: RefreshPolicy da UI com request context que
		// morreu) não é falha do servidor: não entra em backoff nem loga.
		if ctx.Err() != nil {
			return
		}
		s.handleSensorSyncError(err)
		return
	}
	s.applySensorPolicy(agentID, response, true)

	// Recuperação: policy aplicada sem o CORPO de um script de sensor (ex.: cache
	// local corrompido). Um segundo sync com IncludeScriptContent=true ignora o
	// atalho "up to date" e devolve o conteúdo. Guarda pelo force evita recursão.
	if !force && s.sensorsMissingScriptContent() {
		s.logf("sensores: policy entregue sem conteudo de script; refazendo sync com IncludeScriptContent")
		s.refreshSensors(ctx, true)
	}
}

// sensorsMissingScriptContent reporta se algum sensor de script do cache local
// está sem o corpo do script (impossível de executar).
func (s *Service) sensorsMissingScriptContent() bool {
	s.sensorMu.Lock()
	defer s.sensorMu.Unlock()
	for _, cfg := range s.sensors {
		if cfg.Policy.Script == nil {
			continue
		}
		if strings.TrimSpace(cfg.Policy.Script.Content) == "" {
			return true
		}
	}
	return false
}

// handleSensorSyncError aplica o backoff e (no 404) desabilita o loop.
func (s *Service) handleSensorSyncError(err error) {
	now := time.Now()
	logMessage := ""
	disabled := false

	s.sensorMu.Lock()
	if errors.Is(err, errSensorSyncNotFound) {
		disabled = !s.sensorNotFoundLogged
		s.sensorDisabled = true
		s.sensorSyncBackoffUntil = now.Add(sensorSyncBackoffNotFound)
		s.sensorNotFoundLogged = true
		if disabled {
			logMessage = fmt.Sprintf("sensores: servidor nao expoe /sensors/sync (404) - monitoramento desabilitado, nova tentativa em %s", sensorSyncBackoffNotFound)
		}
	} else {
		s.sensorDisabled = false
		s.sensorSyncBackoffUntil = now.Add(sensorSyncBackoffError)
		logMessage = fmt.Sprintf("sensores: falha no sync da policy: %v", err)
	}
	s.sensorMu.Unlock()

	if logMessage != "" {
		s.logf("%s", logMessage)
	}
}

// SetSensorPolicyForTesting aplica uma policy de sensores SEM passar pela rede
// (uso exclusivo de teste, junto de SetStateForTesting) — permite exercitar o
// run-now a partir do pacote app sem expor o sync HTTP.
func (s *Service) SetSensorPolicyForTesting(agentID string, response *SensorSyncResponse) {
	s.applySensorPolicy(agentID, response, false)
}

// applySensorPolicy troca a policy local pelos sensores recebidos no sync.
func (s *Service) applySensorPolicy(agentID string, response *SensorSyncResponse, persist bool) {
	if response == nil {
		return
	}
	defaultInterval := time.Duration(response.DefaultIntervalSeconds) * time.Second
	if defaultInterval <= 0 {
		defaultInterval = sensorDefaultInterval
	}

	// UpToDate com lista vazia = "o fingerprint que você tem continua válido".
	// Sem esta guarda, o sync periódico zeraria o monitoramento.
	s.sensorMu.Lock()
	keepCurrent := response.UpToDate && len(response.Sensors) == 0 && len(s.sensors) > 0
	s.sensorMu.Unlock()
	if keepCurrent {
		s.sensorMu.Lock()
		s.sensorDisabled = false
		s.sensorNotFoundLogged = false
		s.sensorSyncBackoffUntil = time.Time{}
		s.sensorMu.Unlock()
		return
	}

	configs := make([]SensorConfig, 0, len(response.Sensors))
	validIDs := make(map[string]bool, len(response.Sensors))
	for _, policy := range response.Sensors {
		sensorID := strings.TrimSpace(policy.SensorID)
		if sensorID == "" {
			continue
		}
		validIDs[sensorID] = true
		configs = append(configs, buildSensorConfig(policy, defaultInterval))
	}

	fingerprint := strings.TrimSpace(response.SensorFingerprint)
	s.sensorMu.Lock()
	s.sensors = configs
	if fingerprint != "" {
		s.sensorFingerprint = fingerprint
	}
	s.sensorDisabled = false
	s.sensorNotFoundLogged = false
	s.sensorSyncBackoffUntil = time.Time{}
	// Poda órfãos: sensor removido do servidor não pode continuar segurando
	// marcadores (o cleanupOrphanedMarkers os apagaria de qualquer forma).
	for key := range s.sensorLastRun {
		sensorID, _ := splitSensorRunKey(key)
		if !validIDs[sensorID] {
			delete(s.sensorLastRun, key)
		}
	}
	s.sensorMu.Unlock()

	s.loadSensorLastRunsFromMarkers(configs)

	if persist && s.db != nil && agentID != "" {
		snapshot := PersistedSensorPolicy{
			Fingerprint:            fingerprint,
			DefaultIntervalSeconds: response.DefaultIntervalSeconds,
			Sensors:                response.Sensors,
			SavedAt:                time.Now().UTC().Format(time.RFC3339),
		}
		if payload, err := json.Marshal(snapshot); err == nil {
			if err := s.db.SaveSensorConfig(agentID, fingerprint, payload); err != nil {
				s.logf("sensores: falha ao persistir policy local: %v", err)
			}
		}
	}

	s.logf("sensores: policy aplicada (sensores=%d upToDate=%t)", len(configs), response.UpToDate)
}

// loadSensorLastRunsFromMarkers recupera o último disparo persistido para
// sensores que ainda não têm estado em memória (startup do agente).
func (s *Service) loadSensorLastRunsFromMarkers(configs []SensorConfig) {
	if s.db == nil {
		return
	}
	agentID := strings.TrimSpace(s.getConfig().AgentID)
	if agentID == "" {
		return
	}
	for _, cfg := range configs {
		sensorID := strings.TrimSpace(cfg.Policy.SensorID)
		if sensorID == "" {
			continue
		}
		s.sensorMu.Lock()
		_, known := s.sensorLastRun[sensorRunKey(sensorID, "")]
		s.sensorMu.Unlock()
		if known {
			continue
		}
		value, found, err := s.db.GetAutomationMarker(agentID, sensorLastRunMarkerKey(sensorID, ""))
		if err != nil || !found {
			continue
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
		if parseErr != nil {
			continue
		}
		s.sensorMu.Lock()
		if s.sensorLastRun == nil {
			s.sensorLastRun = make(map[string]time.Time)
		}
		s.sensorLastRun[sensorRunKey(sensorID, "")] = parsed
		s.sensorMu.Unlock()
	}
}

// ── Loop ──

// runSensorsLoop é o motor periódico. Roda na própria goroutine, iniciada pelo
// Service.Run junto do runCallbackLoop.
func (s *Service) runSensorsLoop(ctx context.Context, onBeat func()) {
	scheduleTicker := time.NewTicker(sensorScheduleTick)
	defer scheduleTicker.Stop()
	flushTicker := time.NewTicker(sensorFlushInterval)
	defer flushTicker.Stop()

	// Arranque: recupera a policy local (continuidade após restart) e faz o
	// primeiro sync. Feito DENTRO da goroutine do loop para não atrasar o Run.
	s.ensurePersistedSensorsLoaded()
	s.refreshSensors(ctx, true)
	s.dispatchDueSensors(ctx)

	for {
		select {
		case <-ctx.Done():
			// Última entrega (best-effort): não perder o último ciclo coletado.
			flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.flushSensorSamples(flushCtx)
			cancel()
			return
		case <-s.sensorWake:
			s.dispatchDueSensors(ctx)
		case <-scheduleTicker.C:
			if onBeat != nil {
				onBeat()
			}
			s.dispatchDueSensors(ctx)
		case <-flushTicker.C:
			s.flushSensorSamples(ctx)
		}
	}
}

// dispatchDueSensors dispara as coletas devidas respeitando o semáforo.
func (s *Service) dispatchDueSensors(ctx context.Context) {
	sensors, lastRun, disabled := s.sensorStateSnapshot()
	if disabled || len(sensors) == 0 {
		return
	}
	now := time.Now()
	for _, cfg := range sensors {
		if ctx.Err() != nil {
			return
		}
		key := sensorRunKey(cfg.Policy.SensorID, "")
		if !sensorDue(now, lastRun[key], cfg.Interval, cfg.Jitter) {
			continue
		}
		if !s.acquireSensorSlot() {
			// Semáforo cheio: as coletas em voo terminam e o próximo tick
			// reagenda (o lastRun só é marcado quando o slot é obtido).
			return
		}
		s.markSensorDispatched(cfg, now)
		go func(config SensorConfig) {
			defer s.releaseSensorSlot()
			s.runSensor(ctx, config)
		}(cfg)
	}
}

// acquireSensorSlot tenta pegar uma vaga de coleta sem bloquear.
func (s *Service) acquireSensorSlot() bool {
	if s.sensorSem == nil {
		return true
	}
	select {
	case s.sensorSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releaseSensorSlot() {
	if s.sensorSem == nil {
		return
	}
	select {
	case <-s.sensorSem:
	default:
	}
}

// runSensor executa a coleta de UM sensor e enfileira as amostras.
func (s *Service) runSensor(parent context.Context, cfg SensorConfig) {
	timeout := cfg.Timeout
	if timeout <= 0 || timeout > sensorMaxTimeout {
		timeout = sensorMaxTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	started := time.Now()
	runID := uuid.NewString()
	collections := collectSensor(ctx, &cfg)
	finished := time.Now()
	durationMs := int(finished.Sub(started).Milliseconds())

	if len(collections) == 0 {
		collections = []sensorCollection{{Err: "coleta nao produziu evidencia"}}
	}

	sensorID := strings.TrimSpace(cfg.Policy.SensorID)
	results := make([]SensorResult, 0, len(collections))
	for _, collection := range collections {
		targetKey := strings.TrimSpace(collection.TargetKey)
		results = append(results, SensorResult{
			SensorID:        sensorID,
			TargetKey:       targetKey,
			RunID:           runID,
			StartedAt:       started.UTC().Format(time.RFC3339Nano),
			FinishedAt:      finished.UTC().Format(time.RFC3339Nano),
			DurationMs:      sensorInt(durationMs),
			Measure:         collection.Measure,
			Channels:        collection.Channels,
			Error:           collection.Err,
			DeclaredStatus:  collection.DeclaredStatus,
			DeclaredMessage: collection.DeclaredMessage,
		})
		if targetKey != "" {
			s.recordSensorTargetRun(sensorID, targetKey, finished)
		}
	}
	s.recordSensorHistory(results)
	s.enqueueSensorResults(results)

	// Log só de erro: um sensor de disco com 6 volumes geraria 6 linhas por
	// ciclo no log do agente — ruído puro.
	for _, collection := range collections {
		if strings.TrimSpace(collection.Err) != "" {
			s.logf("sensores: coleta falhou (sensor=%q tipo=%s alvo=%q): %s",
				cfg.Policy.Name, cfg.Policy.SensorType, collection.TargetKey, collection.Err)
		}
	}
}

// ── Entrega (buffer + outbox) ──

// enqueueSensorResults adiciona amostras ao buffer de envio.
func (s *Service) enqueueSensorResults(results []SensorResult) {
	if len(results) == 0 {
		return
	}
	s.sensorMu.Lock()
	defer s.sensorMu.Unlock()
	s.sensorPending = append(s.sensorPending, results...)
}

// takeSensorBatch remove até limit amostras do buffer (FIFO).
func (s *Service) takeSensorBatch(limit int) []SensorResult {
	if limit <= 0 {
		limit = sensorResultBatchSize
	}
	s.sensorMu.Lock()
	defer s.sensorMu.Unlock()
	if len(s.sensorPending) == 0 {
		return nil
	}
	if len(s.sensorPending) <= limit {
		batch := s.sensorPending
		s.sensorPending = nil
		return batch
	}
	batch := make([]SensorResult, limit)
	copy(batch, s.sensorPending[:limit])
	s.sensorPending = append([]SensorResult(nil), s.sensorPending[limit:]...)
	return batch
}

// flushSensorSamples entrega o buffer em lotes de até 100. Falha de entrega cai
// na outbox durável (sendOrQueueCallback) e o lote NÃO é re-tentado em rajada
// aqui: o next_attempt_at da fila cuida do retry.
func (s *Service) flushSensorSamples(ctx context.Context) {
	if s == nil || s.client == nil || s.getConfig == nil {
		return
	}
	// Serializa flush (loop + run-now + shutdown) para não duplicar lote.
	s.sensorFlushMu.Lock()
	defer s.sensorFlushMu.Unlock()

	cfg := s.getConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" {
		return
	}
	for {
		batch := s.takeSensorBatch(sensorResultBatchSize)
		if len(batch) == 0 {
			return
		}
		correlationID := uuid.NewString()
		payload := SensorResultBatch{
			AgentID:       agentID,
			CorrelationID: correlationID,
			Samples:       batch,
		}
		if err := s.sendOrQueueCallback(ctx, agentID, correlationID, correlationID, CallbackTypeSensorSamples, payload, correlationID); err != nil {
			s.logf("sensores: falha ao entregar lote de %d amostra(s): %v", len(batch), err)
			return
		}
	}
}

// ── Run-now ──

// RunSensorsNow força a coleta imediata dos sensores informados (ids vazio =
// todos), com sync opcional antes (o console acabou de editar o sensor).
// Devolve quantos sensores foram agendados.
//
// Agendar ZERO sensores NÃO é falha: é resultado válido. Isso acontece nos
// casos mais comuns do operador — policy ainda não sincronizada (s.sensors
// vazio) ou ids que não existem na policy local. Devolver erro aqui virava
// HTTP 500 no painel de debug e no comando "runsensors"; o motivo concreto é
// responsabilidade da camada de apresentação, que já monta a mensagem PT-BR
// ("0 de N id(s) encontrados na policy local") a partir da contagem.
// O erro fica reservado para indisponibilidade real (serviço nulo).
func (s *Service) RunSensorsNow(ctx context.Context, ids []string, forceSync bool) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("servico de automacao indisponivel")
	}
	s.ensurePersistedSensorsLoaded()
	if forceSync {
		s.refreshSensors(ctx, true)
	}
	count := s.markSensorsDue(ids)
	if count == 0 {
		// Nada a acordar: o loop não tem sensor devido. Retorno explícito para
		// deixar claro que a contagem 0 é intencional.
		return 0, nil
	}
	s.signalSensorWake()
	return count, nil
}

// sensorStateForTests expõe o estado local (uso exclusivo de testes).
func (s *Service) sensorStateForTests() ([]SensorConfig, map[string]time.Time, bool) {
	return s.sensorStateSnapshot()
}
