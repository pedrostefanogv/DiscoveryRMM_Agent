package inventory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"discovery/app/core/thirdparty"
	"discovery/app/core/windowsupdate"
	"discovery/app/debug"
	"discovery/app/netutil"
)

const (
	windowsUpdateUploadTimeout = 120 * time.Second
	windowsUpdateEndpointPath  = "/api/v1/agent-auth/me/windows-updates"
	pendingRebootEndpointPath  = "/api/v1/agent-auth/me/pending-reboot"
)

// WindowsUpdateCache persiste o ultimo scan/sonda no SQLite local.
type WindowsUpdateCache interface {
	GetWindowsUpdateCache(agentID string) (payload []byte, hash string, scannedAt time.Time, found bool, err error)
	SaveWindowsUpdateCache(agentID, hash string, payload []byte, scannedAt time.Time) error
	GetWindowsUpdateRebootState(agentID string) (reasons []string, checkedAt time.Time, uploadedAt time.Time, found bool, err error)
	SaveWindowsUpdateRebootState(agentID string, reasons []string, checkedAt time.Time, uploaded bool) error
}

// WindowsUpdateDeps agrupa as dependencias do gerenciador de patches.
type WindowsUpdateDeps struct {
	Client windowsupdate.Client
	Cache  WindowsUpdateCache
	// Enabled e o kill switch (server config). nil = habilitado.
	Enabled func() bool
}

// SetThirdPartyProviders injeta os gerenciadores de apps de terceiros
// (Chocolatey/winget) usados no inventario e no upgrade — WU-19/WU-20.
func (s *Service) SetThirdPartyProviders(providers []thirdparty.Provider) {
	if s == nil {
		return
	}
	s.wuMu.Lock()
	defer s.wuMu.Unlock()
	s.thirdPartyProviders = providers
}

func (s *Service) thirdPartyProvidersSnapshot() []thirdparty.Provider {
	s.wuMu.Lock()
	defer s.wuMu.Unlock()
	return s.thirdPartyProviders
}

// SetWindowsUpdateDeps injeta as dependencias do modulo de patches. Chamado
// pelo App quando o SQLite esta disponivel.
func (s *Service) SetWindowsUpdateDeps(deps WindowsUpdateDeps) {
	if s == nil {
		return
	}
	s.wuMu.Lock()
	defer s.wuMu.Unlock()
	if deps.Client != nil {
		s.wuClient = deps.Client
	}
	if deps.Cache != nil {
		s.wuCache = deps.Cache
	}
	if deps.Enabled != nil {
		s.wuEnabled = deps.Enabled
	}
}

// WindowsUpdateSupported informa se o modulo esta habilitado e tem cliente.
func (s *Service) WindowsUpdateSupported() bool {
	if s == nil {
		return false
	}
	s.wuMu.Lock()
	defer s.wuMu.Unlock()
	return s.wuClientEnabledLocked()
}

func (s *Service) wuClientEnabledLocked() bool {
	if s.wuClient == nil {
		return false
	}
	if s.wuEnabled != nil && !s.wuEnabled() {
		return false
	}
	return true
}

func (s *Service) wuDeps() (windowsupdate.Client, WindowsUpdateCache, bool) {
	s.wuMu.Lock()
	defer s.wuMu.Unlock()
	return s.wuClient, s.wuCache, s.wuClientEnabledLocked()
}

// WindowsUpdateProbe reporta suporte/servico/politica sem scan.
func (s *Service) WindowsUpdateProbe(ctx context.Context) windowsupdate.ProbeInfo {
	client, _, enabled := s.wuDeps()
	if client == nil {
		return windowsupdate.ProbeInfo{Supported: false, ErrorCode: windowsupdate.ScanErrorUnsupported}
	}
	if !enabled {
		return windowsupdate.ProbeInfo{Supported: false, ErrorCode: windowsupdate.ScanErrorUnsupported}
	}
	return client.Probe(ctx)
}

// ScanWindowsUpdates executa o scan WUA (pendentes + instaladas + sonda de
// reboot). Serializado por mutex: comandos e o loop periodico nao podem rodar
// dois scans ao mesmo tempo.
func (s *Service) ScanWindowsUpdates(ctx context.Context) (*windowsupdate.ScanResult, error) {
	_ = ctx // o Scan do cliente aplica o proprio ScanTimeout; ctx e do ciclo de vida
	result, err := s.scanWindowsUpdates(true)
	if err != nil && result == nil {
		return nil, err
	}
	return result, err
}

// scanWindowsUpdates devolve SEMPRE o resultado quando o cliente o forneceu,
// inclusive junto de erro (servico desabilitado/WUA ausente): o chamador usa
// esse resultado parcial para subir o envelope com status=error + errorCode +
// sonda de estado, explicando a maquina na UI. Sem isso a aba ficava vazia e
// sem motivo.
func (s *Service) scanWindowsUpdates(verbose bool) (*windowsupdate.ScanResult, error) {
	client, _, enabled := s.wuDeps()
	if client == nil {
		return nil, windowsupdate.ErrUnsupportedOS
	}
	if !enabled {
		return nil, windowsupdate.ErrUnsupportedOS
	}

	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	if verbose {
		s.logf("[windows-update] scan iniciado")
	}
	started := time.Now()
	result, err := client.Scan(context.Background(), windowsupdate.ScanOptions{IncludeInstalled: true})
	if result == nil {
		if err != nil {
			s.logf("[windows-update] scan falhou (" + ScanErrorCode(err) + "): " + err.Error())
		}
		return nil, err
	}
	if err != nil {
		s.logf(fmt.Sprintf("[windows-update] scan parcial (%s): %v — enviando estado mesmo assim",
			ScanErrorCode(err), err))
	}
	// WU-19/WU-20: apps de terceiros entram no MESMO inventario. Falha de um
	// provider (choco ausente, winget antigo) nao invalida o scan do WUA.
	if thirdParty, failures := s.collectThirdPartyUpdates(context.Background()); len(thirdParty) > 0 || len(failures) > 0 {
		for source, failure := range failures {
			s.logf("[windows-update] inventario de terceiros: provider " + source + " falhou: " + failure.Error())
		}
		if len(thirdParty) > 0 {
			result.Updates = append(result.Updates, thirdParty...)
			result.PendingCount += len(thirdParty)
			s.logf(fmt.Sprintf("[windows-update] inventario de terceiros: %d app(s) com atualizacao", len(thirdParty)))
		}
	}

	waasFeatureUpdates := 0
	for _, update := range result.Updates {
		if strings.EqualFold(update.Source, "waas") && update.IsPending {
			waasFeatureUpdates++
		}
	}
	if waasFeatureUpdates > 0 {
		s.logf(fmt.Sprintf("[windows-update] upgrade de versao (WaaS) no inventario: %d item(ns) informativo(s)", waasFeatureUpdates))
	}

	skipped := ""
	if len(result.SkippedReasons) > 0 {
		skipped = fmt.Sprintf(" reasons=%v", result.SkippedReasons)
	}
	if result.UnreadableErr != "" {
		skipped += " unreadableErr=" + result.UnreadableErr
	}
	s.logf(fmt.Sprintf("[windows-update] scan concluido: pending=%d installed=%d skipped=%d%s reboot=%t(%v) service=%s wuOnline=%t lastInstallSuccess=%s (%dms, total %dms)",
		result.PendingCount, result.InstalledCount, result.SkippedCount, skipped,
		result.PendingReboot.Pending, result.PendingReboot.Reasons,
		result.State.ServiceState, result.State.WuOnline, formatStateTime(result.State.LastInstallSuccess),
		result.DurationMs, time.Since(started).Milliseconds()))
	return result, err
}

// ProbeWindowsUpdateState devolve a sonda barata do estado local (sem WUA) e a
// sua assinatura estavel. Usada pelo loop do agente para decidir quando um
// scan completo e necessario.
func (s *Service) ProbeWindowsUpdateState(ctx context.Context) (windowsupdate.WindowsUpdateState, string) {
	client, _, enabled := s.wuDeps()
	if client == nil || !enabled {
		state := windowsupdate.WindowsUpdateState{
			Supported:       false,
			PolicyErrorCode: windowsupdate.ScanErrorUnsupported,
			CheckedAt:       time.Now().UTC(),
		}
		return state, windowsupdate.WindowsUpdateStateSignature(state)
	}
	state := client.ProbeState(ctx)
	return state, windowsupdate.WindowsUpdateStateSignature(state)
}

// WindowsUpdateStateSignature devolve somente a assinatura (sem alocar estado
// para log quando nao ha mudanca).
func (s *Service) WindowsUpdateStateSignature(ctx context.Context) string {
	_, signature := s.ProbeWindowsUpdateState(ctx)
	return signature
}

// WindowsUpdateSignatures devolve a assinatura normal e a ESTAVEL do estado
// atual, numa unica sonda. A estavel ignora os campos volateis de download e
// alimenta o piso de scan durante download (ver PollWindowsUpdateChanges).
func (s *Service) WindowsUpdateSignatures(ctx context.Context) (string, string) {
	if s == nil {
		return "", ""
	}
	state, signature := s.ProbeWindowsUpdateState(ctx)
	return signature, windowsupdate.WindowsUpdateStateStableSignature(state)
}

func formatStateTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

// SyncWindowsUpdates faz o upload do resultado do scan persiste o cache local.
//
// A supressao por hash NUNCA pode esconder uma mudanca de estado:
//   - scan.status e scan.errorCode mudando sempre força o upload (antes, uma
//     maquina com servico desabilitado/WUA quebrado e as mesmas KBs ficava
//     horas sem reportar a falha);
//   - reboot pendente mudando força o upload (download concluido + reboot
//     pendente NAO mudam a lista de KBs — era exatamente o caso "Baixando 0% /
//     Instalacao pendente" congelado por ate 6h);
//   - o estado local (WindowsUpdateState) muda a assinatura quando o Windows
//     instala/baixa algo.
//
// Nunca falha o ciclo de inventario: erro so loga.
func (s *Service) SyncWindowsUpdates(ctx context.Context, result *windowsupdate.ScanResult) bool {
	return s.syncWindowsUpdates(ctx, result, "")
}

// SyncWindowsUpdatesWithError permite ao chamador informar o erro do scan; o
// envelope sobe com status=error + errorCode mesmo sem resultado completo.
func (s *Service) SyncWindowsUpdatesWithError(ctx context.Context, result *windowsupdate.ScanResult, scanErr error) bool {
	return s.syncWindowsUpdates(ctx, result, ScanErrorCode(scanErr))
}

func (s *Service) syncWindowsUpdates(ctx context.Context, result *windowsupdate.ScanResult, scanErrCode string) bool {
	if s == nil || result == nil {
		return false
	}
	_, cache, enabled := s.wuDeps()
	if !enabled {
		s.logf("[windows-update] upload ignorado: modulo desabilitado por configuracao")
		return false
	}

	cfg := s.debugConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" {
		s.logf("[windows-update] upload ignorado: agentId ausente")
		return false
	}
	if !hasRemoteCredentials(cfg) {
		s.logf("[windows-update] upload ignorado: credenciais ausentes")
		return false
	}

	envelope := buildWindowsUpdateEnvelope(agentID, result, scanErrCode)
	fullBody, err := json.Marshal(envelope)
	if err != nil {
		s.logf("[windows-update] falha ao serializar payload: " + err.Error())
		return false
	}
	hash := hashPayload(fullBody)

	var cachedPayload []byte
	cacheFound := false
	if cache != nil {
		if payload, cachedHash, _, found, cacheErr := cache.GetWindowsUpdateCache(agentID); cacheErr == nil && found {
			cachedPayload = payload
			cacheFound = true
			if cachedHash == hash && !windowsUpdateUploadForced(envelope, cachedPayload) {
				s.logf(fmt.Sprintf("[windows-update] upload ignorado: payload sem mudancas (status=%s reboot=%t updates=%d)",
					envelope.Scan.Status, envelope.PendingReboot.Pending, len(envelope.Updates)))
				return false
			}
			if cachedHash == hash {
				s.logf("[windows-update] upload forçado: status do scan ou reinicio pendente mudou com payload igual")
			}
		}
	}

	// WU-13: quando JA vamos enviar e o trigger e "change", tenta reduzir o
	// payload ao diff. O cache local continua guardando o payload COMPLETO —
	// e ele que serve de referencia para o proximo diff.
	body := fullBody
	if cacheFound {
		plan := PlanWindowsUpdateDelta(envelope.Updates, cachedPayload, envelope.Scan.Trigger)
		if plan.UseDelta {
			// Delta vazio ainda pode ser necessário: o upload foi decidido por
			// hash/forced, e o que mudou foram status/sonda/reboot — o envelope
			// leva esses blocos mesmo sem updates alterados.
			deltaEnvelope := envelope
			deltaEnvelope.Delta = true
			deltaEnvelope.Updates = plan.Changed
			deltaEnvelope.RemovedKeys = plan.Removed
			if deltaBody, marshalErr := json.Marshal(deltaEnvelope); marshalErr == nil {
				body = deltaBody
				s.logf(fmt.Sprintf("[windows-update] envio DELTA: changed=%d removed=%d (full=%d)",
					len(plan.Changed), len(plan.Removed), len(envelope.Updates)))
			}
		}
	}

	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + windowsUpdateEndpointPath
	if err := s.sendWindowsUpdateRequest(ctx, endpoint, cfg, http.MethodPost, body); err != nil {
		s.logf("[windows-update] POST " + endpoint + " falhou: " + err.Error())
		if err := s.sendWindowsUpdateRequest(ctx, endpoint, cfg, http.MethodPut, body); err != nil {
			s.logf("[windows-update] PUT " + endpoint + " falhou: " + err.Error())
			// Nao grava o cache: a proxima tentativa precisa reenviar.
			return false
		}
	}

	if cache != nil {
		// Sempre o payload COMPLETO: o delta e so transporte.
		if err := cache.SaveWindowsUpdateCache(agentID, hash, fullBody, result.StartedAt); err != nil {
			s.logf("[windows-update] aviso: falha ao gravar cache local: " + err.Error())
		}
	}
	s.logf(fmt.Sprintf("[windows-update] inventario enviado: status=%s errorCode=%q updates=%d pending=%d installed=%d skipped=%d reboot=%t(%v) service=%s",
		envelope.Scan.Status, envelope.Scan.ErrorCode, len(result.Updates), result.PendingCount, result.InstalledCount,
		result.SkippedCount, envelope.PendingReboot.Pending, envelope.PendingReboot.Reasons, envelope.State.ServiceState))
	return true
}

// windowsUpdateUploadForced compara o envelope atual com o payload cacheado e
// decide se uma mudanca IMPORTANTE aconteceu mesmo com hash igual (diferenca
// de versao do agente no formato, por exemplo) — ou, no sentido inverso, se o
// hash igual esconde status/reboot diferentes.
func windowsUpdateUploadForced(current agentWindowsUpdateEnvelope, cachedPayload []byte) bool {
	if len(cachedPayload) == 0 {
		return false
	}
	var previous agentWindowsUpdateEnvelope
	if err := json.Unmarshal(cachedPayload, &previous); err != nil {
		return true
	}
	if previous.Scan.Status != current.Scan.Status || previous.Scan.ErrorCode != current.Scan.ErrorCode {
		return true
	}
	if previous.PendingReboot.Pending != current.PendingReboot.Pending ||
		!sameReasons(previous.PendingReboot.Reasons, current.PendingReboot.Reasons) {
		return true
	}
	if previous.State.Signature != current.State.Signature {
		return true
	}
	// As duas sessoes de log/reboot podem estar mais velhas que a sonda leve: se
	// o Windows instalou algo desde o ultimo upload, reenvia so pela data (a
	// lista de KBs pode nao ter mudado — QFE/Event Log ainda nao atualizados).
	if previous.State.LastInstallTime != current.State.LastInstallTime ||
		previous.State.LastInstallSuccess != current.State.LastInstallSuccess {
		return true
	}
	return false
}

// ReportPendingReboot envia a sonda de reinicio SOMENTE quando o estado muda
// em relacao ao ultimo upload. Usado pelo loop de 10 min e apos instalacoes.
func (s *Service) ReportPendingReboot(ctx context.Context) (bool, error) {
	if s == nil {
		return false, nil
	}
	_, cache, enabled := s.wuDeps()
	if !enabled {
		return false, nil
	}

	cfg := s.debugConfig()
	agentID := strings.TrimSpace(cfg.AgentID)
	if agentID == "" || !hasRemoteCredentials(cfg) {
		return false, nil
	}

	info := windowsupdate.ProbePendingReboot()
	if cache != nil {
		reasons, checkedAt, _, found, err := cache.GetWindowsUpdateRebootState(agentID)
		if err == nil && found {
			same := info.Pending == (len(reasons) > 0) && sameReasons(reasons, info.Reasons)
			// Reenvia no maximo a cada 6h para o servidor ter um "checkedAt" fresco.
			if same && time.Since(checkedAt) < 6*time.Hour {
				return false, nil
			}
		}
	}

	payload := struct {
		Pending   bool     `json:"pending"`
		Reasons   []string `json:"reasons,omitempty"`
		CheckedAt string   `json:"checkedAt"`
	}{
		Pending:   info.Pending,
		Reasons:   info.Reasons,
		CheckedAt: info.CheckedAt.UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	endpoint := cfg.ApiScheme + "://" + cfg.ApiServer + pendingRebootEndpointPath
	if err := s.sendWindowsUpdateRequest(ctx, endpoint, cfg, http.MethodPost, body); err != nil {
		s.logf("[windows-update] POST " + endpoint + " (pending-reboot) falhou: " + err.Error())
		return false, err
	}
	s.logf(fmt.Sprintf("[windows-update] sonda de reinicio enviada: pending=%t reasons=%v", info.Pending, info.Reasons))
	if cache != nil {
		if err := cache.SaveWindowsUpdateRebootState(agentID, info.Reasons, info.CheckedAt, true); err != nil {
			s.logf("[windows-update] aviso: falha ao gravar estado de reboot: " + err.Error())
		}
	}
	return true, nil
}

// ScanAndSyncWindowsUpdates e o fluxo completo usado pelo comando remoto e
// pelo loop periodico: scan + upload (+ sonda). Devolve o resultado e o erro do
// scan.
//
// IMPORTANTE: o envelope e enviado MESMO quando o scan falha (servico
// desabilitado, WUA ausente, timeout). Antes disso o agente ficava em silencio
// e a aba de atualizacoes do agente vazia e sem explicacao.
func (s *Service) ScanAndSyncWindowsUpdates(ctx context.Context) (*windowsupdate.ScanResult, error) {
	SetWindowsUpdateTrigger("command")
	return s.scanAndSync(ctx, "")
}

// ScanAndSyncWindowsUpdatesTriggered e igual a ScanAndSyncWindowsUpdates, mas
// registra o motivo do scan no envelope (periodic | change | command | install).
func (s *Service) ScanAndSyncWindowsUpdatesTriggered(ctx context.Context, trigger string) (*windowsupdate.ScanResult, error) {
	if strings.TrimSpace(trigger) == "" {
		trigger = "command"
	}
	SetWindowsUpdateTrigger(trigger)
	return s.scanAndSync(ctx, "")
}

func (s *Service) scanAndSync(ctx context.Context, _ string) (*windowsupdate.ScanResult, error) {
	result, err := s.ScanWindowsUpdates(ctx)
	if err != nil {
		s.logf("[windows-update] scan falhou (" + ScanErrorCode(err) + "): " + err.Error())
	}
	if result != nil {
		s.SyncWindowsUpdatesWithError(ctx, result, err)
		// O envelope ja levou a sonda de reboot; persiste o estado local nos DOIS
		// sentidos. Antes so gravavamos pending=true: a transicao true->false
		// (reinicio feito ou limpo pelo Windows) ficava com pipe antigo e a sonda
		// leve continuava anunciando "reinicio pendente" na UI.
		if _, cache, _ := s.wuDeps(); cache != nil {
			if cfgAgentID := strings.TrimSpace(s.debugConfig().AgentID); cfgAgentID != "" {
				_ = cache.SaveWindowsUpdateRebootState(cfgAgentID, result.PendingReboot.Reasons, result.PendingReboot.CheckedAt, true)
			}
		}
		return result, err
	}

	// Nenhum resultado do cliente (modulo desabilitado ou cliente ausente):
	// mesmo assim reporta a sonda barata de reinicio, que e o unico sinal que
	// temos para nao deixar o servidor cego.
	if _, reportErr := s.ReportPendingReboot(ctx); reportErr != nil {
		s.logf("[windows-update] falha ao reportar reinicio pendente: " + reportErr.Error())
	}
	if err == nil {
		err = windowsupdate.ErrUnsupportedOS
	}
	return nil, err
}

// InstallWindowsUpdates instala os alvos informados (ja revalidados no WUA pelo
// cliente) e, em seguida, atualiza o inventario local/servidor.
func (s *Service) InstallWindowsUpdates(ctx context.Context, targets []windowsupdate.InstallTarget) (*windowsupdate.InstallResult, error) {
	client, _, enabled := s.wuDeps()
	if client == nil || !enabled {
		return nil, windowsupdate.ErrUnsupportedOS
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	// WU-06/D07: preflight ANTES de qualquer mutacao. Servico parado, disco
	// insuficiente ou bateria (quando a politica bloqueia) viram motivo
	// estruturado — nunca um erro obscuro no meio do download.
	//
	// blockOnBattery=false por padrao (D07: fase 1 apenas avisa); o piso de
	// disco cobre o maior cumulative update tipico.
	if failure, onBattery := windowsupdate.RunPreflight(0, false); failure != nil {
		s.logf("[windows-update] preflight barrou a instalacao: " + failure.Error())
		return nil, failure
	} else if onBattery {
		s.logf("[windows-update] aviso: instalando com a maquina na bateria")
	}

	result, err := client.Install(ctx, targets)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// HideWindowsUpdates oculta/reexibe KBs no WUA (enforcement no SO, opt-in).
func (s *Service) HideWindowsUpdates(ctx context.Context, keys []string, hidden bool) (int, error) {
	client, _, enabled := s.wuDeps()
	if client == nil || !enabled {
		return 0, windowsupdate.ErrUnsupportedOS
	}
	return client.Hide(ctx, keys, hidden)
}

// ApplyWindowsUpdatePolicy aplica a politica de WU (opt-in) e devolve o backup.
func (s *Service) ApplyWindowsUpdatePolicy(settings windowsupdate.PolicySettings) (*windowsupdate.PolicyBackup, error) {
	if s == nil {
		return nil, windowsupdate.ErrUnsupportedOS
	}
	return windowsupdate.ApplyPolicy(settings)
}

// RestoreWindowsUpdatePolicy restaura o estado anterior da politica.
func (s *Service) RestoreWindowsUpdatePolicy(backup windowsupdate.PolicyBackup) error {
	return windowsupdate.RestorePolicy(backup)
}

// ReadWindowsUpdatePolicy le a politica atual.
func (s *Service) ReadWindowsUpdatePolicy() (windowsupdate.PolicySettings, error) {
	return windowsupdate.ReadPolicy()
}

// ---- envelope/upload helpers ----------------------------------------------

type agentWindowsUpdateScan struct {
	Status     string `json:"status"`
	DurationMs int64  `json:"durationMs"`
	StartedAt  string `json:"startedAt"`
	ErrorCode  string `json:"errorCode,omitempty"`
	// SkippedCount vai SEMPRE (inclusive 0): o servidor precisa poder LIMPAR um
	// contador antigo quando o scan passa a descartar nada. Com omitempty o
	// "3 ignorados" ficaria congelado no snapshot para sempre.
	SkippedCount  int            `json:"skippedCount"`
	SkippedReason map[string]int `json:"skippedReasons,omitempty"`
	// Source: wua/qfe; Trigger: periodic | change | command | install.
	Source  string `json:"source,omitempty"`
	Trigger string `json:"trigger,omitempty"`
}

// agentWindowsUpdateState e o espelho JSON do WindowsUpdateState + assinatura.
// Campos opcionais no servidor: a API antiga ignora o objeto inteiro.
type agentWindowsUpdateState struct {
	Supported          bool     `json:"supported"`
	ServiceState       string   `json:"serviceState,omitempty"`
	WuOnline           bool     `json:"wuOnline"`
	PolicyManaged      bool     `json:"policyManaged"`
	WSUSServer         string   `json:"wsusServer,omitempty"`
	RebootPending      bool     `json:"rebootPending"`
	RebootReasons      []string `json:"rebootReasons,omitempty"`
	LastInstallSuccess string   `json:"lastInstallSuccess,omitempty"`
	LastInstallTime    string   `json:"lastInstallTime,omitempty"`
	DownloadInProgress bool     `json:"downloadInProgress"`
	// DownloadPercent: porcentagem do download em andamento (0-100). Ponteiro
	// para o 0% REAL viajar (é informação válida) e o "desconhecido" ficar
	// ausente. Só é preenchido quando DownloadInProgress.
	DownloadPercent *int `json:"downloadPercent,omitempty"`
	// QFEInstalledCount: ponteiro para distinguir "0 KBs" de "leitura falhou"
	// (ausente = servidor preserva a contagem conhecida).
	QFEInstalledCount *int     `json:"qfeInstalledCount,omitempty"`
	RecentQFEs        []string `json:"recentQfes,omitempty"`
	AuOptions         int      `json:"auOptions,omitempty"`
	NoAutoUpdate      bool     `json:"noAutoUpdate"`
	DeferralDays      int      `json:"deferralDays,omitempty"`
	// ActiveHoursStart/End: janela de horas ativas da politica (0-23). A sonda
	// ja lia esses valores e eles NAO subiam no envelope — a API/site tinham o
	// campo e nunca recebiam.
	ActiveHoursStart *int `json:"activeHoursStart,omitempty"`
	ActiveHoursEnd   *int `json:"activeHoursEnd,omitempty"`
	// PolicyKnown: a politica foi lida (permite LIMPAR valores antigos no
	// servidor quando o WSUS/deferral sai da máquina).
	PolicyKnown bool   `json:"policyKnown"`
	CheckedAt   string `json:"checkedAt"`
	// Signature e o hash estavel do estado: o agente reenvia quando muda.
	Signature string `json:"signature,omitempty"`
}

// qfeInstalledCountValue devolve a contagem de KBs do CBS apenas quando a
// leitura foi confiável (0 KBs reais é válido; falha de leitura é ausente).
func qfeInstalledCountValue(state windowsupdate.WindowsUpdateState) *int {
	if !state.QFEInstalledCountKnown {
		return nil
	}
	count := state.QFEInstalledCount
	if count < 0 {
		return nil
	}
	return &count
}

// downloadPercentValue devolve a porcentagem para o envelope apenas quando há
// download em andamento (0% real incluído). Fora de download, ausente.
func downloadPercentValue(state windowsupdate.WindowsUpdateState) *int {
	if !state.DownloadInProgress {
		return nil
	}
	percent := state.DownloadProgressPercent
	if percent < 0 || percent > 100 {
		return nil
	}
	return &percent
}

func newAgentWindowsUpdateState(state windowsupdate.WindowsUpdateState) agentWindowsUpdateState {
	return agentWindowsUpdateState{
		Supported:          state.Supported,
		ServiceState:       state.ServiceState,
		WuOnline:           state.WuOnline,
		PolicyManaged:      state.PolicyManaged,
		WSUSServer:         state.WSUSServer,
		RebootPending:      state.RebootPending,
		RebootReasons:      state.RebootReasons,
		LastInstallSuccess: formatStateTime(state.LastInstallSuccess),
		LastInstallTime:    formatStateTime(state.LastInstallTime),
		DownloadInProgress: state.DownloadInProgress,
		DownloadPercent:    downloadPercentValue(state),
		QFEInstalledCount:  qfeInstalledCountValue(state),
		RecentQFEs:         state.RecentQFEs,
		AuOptions:          state.AuOptions,
		NoAutoUpdate:       state.NoAutoUpdate,
		DeferralDays:       state.DeferralDays,
		ActiveHoursStart:   state.ActiveHoursStart,
		ActiveHoursEnd:     state.ActiveHoursEnd,
		PolicyKnown:        state.PolicyKnown,
		CheckedAt:          state.CheckedAt.UTC().Format(time.RFC3339),
		Signature:          windowsupdate.WindowsUpdateStateSignature(state),
	}
}

type agentWindowsUpdateReboot struct {
	Pending   bool     `json:"pending"`
	Reasons   []string `json:"reasons,omitempty"`
	CheckedAt string   `json:"checkedAt"`
}

type agentWindowsUpdateEnvelope struct {
	AgentID     string `json:"agentId"`
	CollectedAt string `json:"collectedAt"`
	// EnvelopeVersion/Capabilities (WU-13, D06): contrato explicito para a API
	// decidir se entende delta e campos novos. Agente antigo nao envia e a API
	// cai no caminho completo.
	EnvelopeVersion int                      `json:"envelopeVersion"`
	Capabilities    []string                 `json:"capabilities,omitempty"`
	Scan            agentWindowsUpdateScan   `json:"scan"`
	PendingReboot   agentWindowsUpdateReboot `json:"pendingReboot"`
	Updates         []windowsupdate.Update   `json:"updates"`
	// Delta=true: Updates contem apenas os itens alterados e RemovedKeys lista o
	// que saiu desde o ultimo full (WU-13).
	Delta       bool     `json:"delta,omitempty"`
	RemovedKeys []string `json:"removedKeys,omitempty"`
	// State e a sonda barata do estado local (campo OPCIONAL: API antiga ignora).
	State agentWindowsUpdateState `json:"state"`
}

// scanStatusFor mapeia o resultado para o status do envelope. "partial"
// sinaliza que uma fonte falhou (ex.: QFE instaladas) e listas vazias NÃO devem
// ser tratadas como remoção pelo servidor.
func scanStatusFor(result *windowsupdate.ScanResult, scanErrCode string) string {
	switch scanErrCode {
	case windowsupdate.ScanErrorTimeout:
		return windowsupdate.ScanStatusTimeout
	case windowsupdate.ScanErrorUnsupported:
		return windowsupdate.ScanStatusUnsupported
	case windowsupdate.ScanErrorWuaUnavailable, windowsupdate.ScanErrorServiceDisabled, windowsupdate.ScanErrorFailed:
		return windowsupdate.ScanStatusError
	}
	if result != nil && result.Partial {
		return "partial"
	}
	return windowsupdate.ScanStatusOK
}

// buildWindowsUpdateEnvelope monta o payload de upload. scanErrCode != ""
// produz status=error/timeout/unsupported + errorCode SEMPRE — inclusive quando
// o scan nem rodou (servico desabilitado) — para a UI explicar o vazio.
func buildWindowsUpdateEnvelope(agentID string, result *windowsupdate.ScanResult, scanErrCode string) agentWindowsUpdateEnvelope {
	startedAt := result.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	state := result.State
	if state.CheckedAt.IsZero() {
		state.CheckedAt = time.Now().UTC()
	}
	// A sonda de reboot mais fresca e a do scan; mantem coerencia entre
	// pendingReboot e state.rebootPending (duas leituras em momentos diferentes
	// confundiriam a UI).
	state.RebootPending = result.PendingReboot.Pending
	if len(result.PendingReboot.Reasons) > 0 || !result.PendingReboot.Pending {
		state.RebootReasons = append([]string(nil), result.PendingReboot.Reasons...)
	}
	if scanErrCode == "" && strings.EqualFold(state.ServiceState, "disabled") {
		scanErrCode = windowsupdate.ScanErrorServiceDisabled
	}
	if scanErrCode != "" && !result.Partial {
		result.Partial = true
	}

	updates := result.Updates
	if updates == nil {
		updates = []windowsupdate.Update{}
	}
	rebootCheckedAt := result.PendingReboot.CheckedAt
	if rebootCheckedAt.IsZero() {
		rebootCheckedAt = state.CheckedAt
	}
	return agentWindowsUpdateEnvelope{
		AgentID:         agentID,
		CollectedAt:     startedAt.UTC().Format(time.RFC3339),
		EnvelopeVersion: windowsUpdateEnvelopeVersion,
		Capabilities:    windowsUpdateCapabilities,
		Scan: agentWindowsUpdateScan{
			Status:        scanStatusFor(result, scanErrCode),
			DurationMs:    result.DurationMs,
			StartedAt:     startedAt.UTC().Format(time.RFC3339),
			ErrorCode:     scanErrCode,
			SkippedCount:  result.SkippedCount,
			SkippedReason: result.SkippedReasons,
			Source:        result.Source,
			Trigger:       currentWindowsUpdateTrigger(),
		},
		PendingReboot: agentWindowsUpdateReboot{
			Pending:   result.PendingReboot.Pending,
			Reasons:   result.PendingReboot.Reasons,
			CheckedAt: rebootCheckedAt.UTC().Format(time.RFC3339),
		},
		Updates: updates,
		State:   newAgentWindowsUpdateState(state),
	}
}

// windowsUpdateTrigger guarda o motivo do ultimo scan (log/diagnostico). O
// valor entra no envelope para a UI diferenciar "verificar agora" de
// "periodico".
var windowsUpdateTrigger atomic.Value

// SetWindowsUpdateTrigger registra o motivo do proximo upload
// (periodic | change | command | install).
func SetWindowsUpdateTrigger(trigger string) {
	switch strings.ToLower(strings.TrimSpace(trigger)) {
	case "periodic", "change", "command", "install":
		windowsUpdateTrigger.Store(strings.ToLower(strings.TrimSpace(trigger)))
	}
}

func currentWindowsUpdateTrigger() string {
	if value, ok := windowsUpdateTrigger.Load().(string); ok {
		return value
	}
	return ""
}

var windowsUpdateHTTPClient = &http.Client{Timeout: windowsUpdateUploadTimeout}

func (s *Service) sendWindowsUpdateRequest(parent context.Context, endpoint string, cfg debug.Config, method string, body []byte) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, windowsUpdateUploadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return err
	}

	started := time.Now()
	resp, err := windowsUpdateHTTPClient.Do(req)
	if err != nil {
		s.logf(fmt.Sprintf("[windows-update] %s %s falhou apos %dms: %v", method, endpoint, time.Since(started).Milliseconds(), err))
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	elapsed := time.Since(started).Milliseconds()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logf(fmt.Sprintf("[windows-update] %s %s -> HTTP %s (%dms) body=%s", method, endpoint, resp.Status, elapsed, truncateForLog(string(respBody), 512)))
		return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	s.logf(fmt.Sprintf("[windows-update] %s %s -> %d OK (%dms, request=%dB response=%dB)", method, endpoint, resp.StatusCode, elapsed, len(body), len(respBody)))
	return nil
}

// truncateForLog limita o texto de diagnostico (respostas de erro da API).
func truncateForLog(value string, max int) string {
	trimmed := strings.TrimSpace(value)
	if max > 0 && len(trimmed) > max {
		return trimmed[:max] + "..."
	}
	return trimmed
}

func hashPayload(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func hasRemoteCredentials(cfg debug.Config) bool {
	if strings.TrimSpace(cfg.ApiServer) == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(cfg.ApiScheme))
	return scheme == "http" || scheme == "https"
}

func sameReasons(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, value := range a {
		seen[value] = struct{}{}
	}
	for _, value := range b {
		if _, ok := seen[value]; !ok {
			return false
		}
	}
	return true
}

// WindowsUpdateDisabledReason devolve um motivo estavel quando o modulo nao
// pode rodar (kill switch/plataforma).
func (s *Service) WindowsUpdateDisabledReason() string {
	client, _, enabled := s.wuDeps()
	if client == nil {
		return windowsupdate.ScanErrorUnsupported
	}
	if !enabled {
		return windowsupdate.ScanErrorUnsupported
	}
	return ""
}

// ScanErrorCode traduz o erro do scan em codigo estavel para o servidor.
func ScanErrorCode(err error) string {
	switch {
	case err == nil:
		return windowsupdate.ScanErrorNone
	case errors.Is(err, windowsupdate.ErrUnsupportedOS):
		return windowsupdate.ScanErrorUnsupported
	case errors.Is(err, windowsupdate.ErrWuaUnavailable):
		return windowsupdate.ScanErrorWuaUnavailable
	case errors.Is(err, windowsupdate.ErrServiceDisabled):
		return windowsupdate.ScanErrorServiceDisabled
	case errors.Is(err, windowsupdate.ErrTimeout), errors.Is(err, context.DeadlineExceeded):
		return windowsupdate.ScanErrorTimeout
	default:
		return windowsupdate.ScanErrorFailed
	}
}
