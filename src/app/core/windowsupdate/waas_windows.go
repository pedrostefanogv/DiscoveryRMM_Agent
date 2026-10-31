//go:build windows

package windowsupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/registry"
)

// ── WaaS feature assessment (upgrade de versão) ─────────────────────────────
//
// O WUA NAO enumera feature updates/upgrades de versao (comportamento conhecido
// do Windows 11; o proprio Windows Update UI usa outro caminho). O estado do
// servico USO/WaaS grava a avaliacao da plataforma em
// HKLM\...\WindowsUpdate\Orchestrator\WaaSFeatureAssessment (JSON):
//
//   {"nextApplicableReleaseTime":0,"isOutOfDate":true,"isEndOfSupport":false,
//    "status":1,"impact":2,"daysOutOfDate":10}
//
// Quando isOutOfDate/status != Latest, existe uma versao mais nova disponivel
// para o aparelho. Sintetizamos um item de inventario para a aba de atualizacoes
// (source "waas") sem depender do WUA.

const (
	waasOrchestratorKey = `SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\WindowsUpdate\\Orchestrator`
	waasFeatureValue    = "WaaSFeatureAssessment"
	// GUID estavel para o "upgrade de versao" sintetizado. Nao colide com
	// UpdateID real (bloco 1111...); o servidor aceita guid:<uuid>:<rev>.
	waasFeatureUpdateID  = "11111111-2222-3333-4444-555555555555"
	waasFeatureUpdateKey = "guid:" + waasFeatureUpdateID + ":1"
)

// waasFeatureAssessment espelha OSUpdateAssessment/UpdateAssessment do
// WaaSAPI.h (campos serializados pelo servico USO no registro).
type waasFeatureAssessment struct {
	NextApplicableReleaseTime int64  `json:"nextApplicableReleaseTime"`
	IsOutOfDate               bool   `json:"isOutOfDate"`
	IsEndOfSupport            bool   `json:"isEndOfSupport"`
	Status                    int    `json:"status"`
	Impact                    int    `json:"impact"`
	DaysOutOfDate             int    `json:"daysOutOfDate"`
	UpToDateOSBuild           string `json:"upToDateOSBuild"`
}

// featureUpdateAvailable indica se a avaliacao encontrou uma versao mais nova.
// status 0 = Latest; os demais valores sao "NotLatest*" (soft/hard restriction,
// end of support, deferred/paused/targeted/managed...).
func (a waasFeatureAssessment) featureUpdateAvailable() bool {
	return a.IsOutOfDate || a.Status != 0
}

func parseWaaSFeatureAssessment(raw string) (waasFeatureAssessment, bool) {
	var assessment waasFeatureAssessment
	if strings.TrimSpace(raw) == "" {
		return assessment, false
	}
	if err := json.Unmarshal([]byte(raw), &assessment); err != nil {
		return assessment, false
	}
	return assessment, true
}

// ── Fallback: avaliação WaaS ausente ────────────────────────────────────────
//
// O valor WaaSFeatureAssessment só é escrito pelo orquestrador USO após um
// scan interativo. Máquinas onde o UsoSvc nunca rodou (ou está parado há
// muito) não têm o valor — e a UI do Windows Update mostra o upgrade de
// versão por OUTRO caminho, então o RMM exibia "em dia" sem o upgrade (caso
// reproduzido: Win11 25H2 build 26200.9711 com "Windows 11, versão 26H2
// disponível" na tela e WaaSFeatureAssessment AUSENTE).
//
// Estratégia (best-effort, dentro do timeout do scan):
//   1. UsoClient StartInteractiveScan — instrui o USO a avaliar/escanear;
//   2. Poll do registro esperando o valor aparecer (até waasAssessmentWait).

const (
	waasAssessmentWait      = 60 * time.Second // teto do poll (scan custa tempo)
	waasAssessmentPollEvery = 3 * time.Second
	// Teto do proprio UsoClient: ele pode travar esperando a rede/UI. Sem isto
	// o scan inteiro ficaria preso e o withOp devolveria timeout sem resultado.
	waasTriggerTimeout = 30 * time.Second
	// Cooldown entre tentativas FRACASSADAS: em maquinas onde o USO nunca
	// grava a avaliacao (servico desabilitado por politica, SKU sem feature
	// update), nao faz sentido gastar 60s de poll em TODO scan (a cada 6h).
	waasTriggerRetryCooldown = 6 * time.Hour
)

var (
	waasTriggerMu        sync.Mutex
	waasTriggerLastTried time.Time
)

// usoClientPath devolve o caminho do UsoClient.exe (%SystemRoot%\System32).
func usoClientPath() string {
	sysRoot := os.Getenv("SystemRoot")
	if strings.TrimSpace(sysRoot) == "" {
		sysRoot = "C:\\Windows"
	}
	return filepath.Join(sysRoot, "System32", "usoClient.exe")
}

// triggerWaaSScan dispara o scan interativo do USO e espera a avaliação de
// feature update ser gravada. Nunca falha o scan do agente: erro => ok=false.
func triggerWaaSScan(ctx context.Context) (waasFeatureAssessment, bool) {
	// Cooldown: tentativa recente que falhou nao se repete a cada scan.
	waasTriggerMu.Lock()
	if !waasTriggerLastTried.IsZero() && time.Since(waasTriggerLastTried) < waasTriggerRetryCooldown {
		waasTriggerMu.Unlock()
		return waasFeatureAssessment{}, false
	}
	waasTriggerLastTried = time.Now()
	waasTriggerMu.Unlock()

	deadline := time.Now().Add(waasAssessmentWait)
	ctxDeadline := time.Time{}
	if ctx != nil {
		ctxDeadline, _ = ctx.Deadline()
	}
	if !ctxDeadline.IsZero() && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	// Best-effort: UsoClient pode falhar (servico desabilitado, política) ou
	// travar — o timeout curto evita prender o scan; segue com o poll mesmo
	// assim (a avaliação pode chegar por outro ciclo).
	triggerCtx, cancel := context.WithTimeout(context.Background(), waasTriggerTimeout)
	_ = exec.CommandContext(triggerCtx, usoClientPath(), "StartInteractiveScan").Run()
	cancel()

	for {
		if assessment, ok := readWaaSFeatureAssessment(); ok {
			// Sucesso: zera o cooldown para que uma futura perda do valor
			// (reset do USO) volte a disparar o scan imediatamente.
			waasTriggerMu.Lock()
			waasTriggerLastTried = time.Time{}
			waasTriggerMu.Unlock()
			return assessment, true
		}
		if time.Now().After(deadline) {
			return waasFeatureAssessment{}, false
		}
		if ctx != nil && ctx.Err() != nil {
			return waasFeatureAssessment{}, false
		}
		time.Sleep(waasAssessmentPollEvery)
	}
}

// readWaaSFeatureAssessment le a avaliacao do registro. Best-effort: ausencia
// (Windows antigo, servico desabilitado) devolve ok=false.
func readWaaSFeatureAssessment() (waasFeatureAssessment, bool) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, waasOrchestratorKey, registry.QUERY_VALUE)
	if err != nil {
		return waasFeatureAssessment{}, false
	}
	defer key.Close()

	raw, _, err := key.GetStringValue(waasFeatureValue)
	if err != nil {
		return waasFeatureAssessment{}, false
	}
	return parseWaaSFeatureAssessment(raw)
}

// currentDisplayVersion devolve "25H2", "24H2", etc. (best-effort).
func currentDisplayVersion() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer key.Close()
	value, _, err := key.GetStringValue("DisplayVersion")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// featureUpdateFromAssessment monta o item sintetico do inventario.
func featureUpdateFromAssessment(assessment waasFeatureAssessment, currentVersion string) Update {
	title := "Atualizacao de versao do Windows disponivel"
	if currentVersion != "" {
		title = fmt.Sprintf("%s (versao atual %s)", title, currentVersion)
	}
	description := fmt.Sprintf(
		"WaaS feature assessment: status=%d impact=%d daysOutOfDate=%d endOfSupport=%t.",
		assessment.Status, assessment.Impact, assessment.DaysOutOfDate, assessment.IsEndOfSupport)
	if assessment.UpToDateOSBuild != "" {
		description += " Versao recomendada: " + assessment.UpToDateOSBuild + "."
	}

	severity := "Moderate"
	switch assessment.Impact {
	case 0:
		severity = "Low"
	case 1:
		severity = "Low"
	case 2:
		severity = "Moderate"
	case 3:
		severity = "Important"
	}

	return Update{
		UpdateKey:      waasFeatureUpdateKey,
		UpdateID:       waasFeatureUpdateID,
		Revision:       1,
		Title:          truncate(title, 500),
		Description:    truncate(description, 2000),
		Classification: "Upgrades",
		Categories:     []string{"Upgrades"},
		UpdateType:     "feature",
		Severity:       severity,
		SeveritySource: "derived",
		IsPending:      true,
		IsInstalled:    false,
		Source:         "waas",
		PublishedAt:    nil,
	}
}

// appendWaaSFeatureUpdate acrescenta o upgrade sintetico ao scan quando o WUA
// nao trouxe nenhum item de upgrade (evita duplicar quando o Windows passar a
// expor feature updates no WUA).
func appendWaaSFeatureUpdate(updates []Update, assessment waasFeatureAssessment, currentVersion string) []Update {
	if !assessment.featureUpdateAvailable() {
		return updates
	}
	for _, update := range updates {
		if strings.EqualFold(strings.TrimSpace(update.Classification), "Upgrades") ||
			strings.EqualFold(strings.TrimSpace(update.UpdateType), "feature") {
			return updates
		}
	}
	return append(updates, featureUpdateFromAssessment(assessment, currentVersion))
}
