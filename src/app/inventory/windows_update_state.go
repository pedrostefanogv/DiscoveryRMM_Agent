package inventory

// Scan sob demanda dirigido pela sonda de estado.
//
// O scan completo do WUA e caro (~13-15s nesta maquina com 6188 pacotes CBS:
// enumeracao CBS + WUA + QFE) e por isso NAO pode ser o unico caminho para o
// servidor saber que algo mudou. A solucao e uma sonda BARATA e frequente
// (registro/log/SCM, poucos ms):
//
//  1. a cada PollWindowsUpdateChangesInterval (default 5 min) o agente le o
//     estado local e calcula a assinatura (windowsupdate.WindowsUpdateStateSignature);
//  2. se a assinatura mudou em relacao a ultima vista -> dispara o scan
//     completo + upload imediatamente (download concluido, reboot pendente,
//     KB instalada, servico caiu);
//  3. o scan periodico configuravel continua como rede de seguranca, e tambem
//     quando o servidor pede ("Verificar atualizacoes" -> comando).

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"discovery/app/core/windowsupdate"
)

// WindowsUpdatePollResult descreve o outcome de um ciclo da sonda.
type WindowsUpdatePollResult struct {
	Checked       bool
	Signature     string
	Changed       bool
	Scanned       bool
	State         windowsupdate.WindowsUpdateState
	ScanStatus    string
	ScanErrorCode string
	// Trigger: motivo do scan disparado (change | periodic | command | install).
	Trigger string
}

var (
	wuPollMu         sync.Mutex
	wuLastSign       string
	wuLastSignAt     time.Time
	wuLastPollAt     time.Time
	wuLastChangeScanAt time.Time
	wuLastPollInfo   WindowsUpdatePollResult
)

// LastWindowsUpdatePoll devolve o ultimo resultado da sonda (diagnostico/UI).
func LastWindowsUpdatePoll() WindowsUpdatePollResult {
	wuPollMu.Lock()
	defer wuPollMu.Unlock()
	return wuLastPollInfo
}

// PollWindowsUpdateChanges roda UM ciclo da sonda de estado. Quando a
// assinatura muda (ou quando nunca foi vista), executa o scan completo +
// upload com o trigger informado ("change" por padrao).
//
// minScanGap impede tempestades: se o ultimo scan completo rodou ha menos que
// isso, a mudanca e registrada (a assinatura de referencia NAO e atualizada) e
// o scan fica para o proximo ciclo.
func (s *Service) PollWindowsUpdateChanges(ctx context.Context, minScanGap time.Duration, trigger string) WindowsUpdatePollResult {
	if s == nil {
		return WindowsUpdatePollResult{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client, _, enabled := s.wuDeps()
	if client == nil || !enabled {
		return WindowsUpdatePollResult{}
	}

	state := client.ProbeState(ctx)
	signature := windowsupdate.WindowsUpdateStateSignature(state)

	wuPollMu.Lock()
	previous := wuLastSign
	first := previous == ""
	changed := previous != signature
	lastScanAt := wuLastChangeScanAt
	wuLastPollAt = time.Now()
	wuPollMu.Unlock()

	result := WindowsUpdatePollResult{
		Checked:   true,
		Signature: signature,
		Changed:   changed,
		State:     state,
		Trigger:   "change",
	}
	if strings.TrimSpace(trigger) != "" {
		result.Trigger = strings.TrimSpace(trigger)
	}
	if !changed {
		wuPollMu.Lock()
		wuLastPollInfo = result
		wuPollMu.Unlock()
		return result
	}

	s.logf(fmt.Sprintf("[windows-update] sonda de estado%s: assinatura mudou (%s -> %s) service=%s reboot=%t(%v) download=%t lastInstallSuccess=%s",
		firstLabel(first), shortSignature(previous), shortSignature(signature),
		state.ServiceState, state.RebootPending, state.RebootReasons, state.DownloadInProgress,
		formatStateTime(state.LastInstallSuccess)))

	if minScanGap > 0 && !lastScanAt.IsZero() && time.Since(lastScanAt) < minScanGap {
		// Evita scan em rajada; a referencia NAO avanca para que a mudanca seja
		// vista no proximo ciclo.
		s.logf(fmt.Sprintf("[windows-update] scan sob demanda adiado: ultimo scan ha %s (< %s)",
			time.Since(lastScanAt).Round(time.Second), minScanGap))
		wuPollMu.Lock()
		wuLastPollInfo = result
		wuPollMu.Unlock()
		return result
	}

	wuPollMu.Lock()
	wuLastChangeScanAt = time.Now()
	// A referencia avanca JA: se o scan falhar, a proxima mudanca real volta a
	// disparar (a assinatura de falha inclui serviceState/policyError).
	wuLastSign = signature
	wuLastSignAt = wuLastChangeScanAt
	wuPollMu.Unlock()

	// ScanWindowsUpdates respeita scanMu; um scan em andamento (comando do
	// servidor/instalacao) apenas faz este ciclo aguardar.
	scanResult, scanErr := s.ScanAndSyncWindowsUpdatesTriggered(ctx, result.Trigger)
	result.Scanned = scanResult != nil
	result.ScanErrorCode = ScanErrorCode(scanErr)
	if scanResult != nil {
		result.ScanStatus = scanStatusFor(scanResult, result.ScanErrorCode)
	}
	wuPollMu.Lock()
	wuLastPollInfo = result
	wuPollMu.Unlock()
	return result
}

// SetWindowsUpdateBaseline fixa a assinatura atual como referencia SEM rodar
// scan: usado depois de um scan periodico/comando (o estado que acabou de ser
// enviado ao servidor passa a ser a referencia).
func (s *Service) SetWindowsUpdateBaseline(signature string) {
	if strings.TrimSpace(signature) == "" {
		return
	}
	wuPollMu.Lock()
	wuLastSign = signature
	wuLastSignAt = time.Now()
	wuPollMu.Unlock()
}

// WindowsUpdateBaseline devolve a assinatura de referencia atual.
func WindowsUpdateBaseline() string {
	wuPollMu.Lock()
	defer wuPollMu.Unlock()
	return wuLastSign
}

// LastWindowsUpdateSignatureAt informa quando a referencia foi atualizada.
func LastWindowsUpdateSignatureAt() time.Time {
	wuPollMu.Lock()
	defer wuPollMu.Unlock()
	return wuLastSignAt
}

func firstLabel(first bool) string {
	if first {
		return " (primeira leitura)"
	}
	return ""
}

func shortSignature(signature string) string {
	if signature == "" {
		return "<vazio>"
	}
	if len(signature) > 12 {
		return signature[:12]
	}
	return signature
}
