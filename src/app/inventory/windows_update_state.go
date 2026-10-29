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

// Estado da sonda compartilhado pelo processo. As assinaturas ficam por
// agente: o mesmo processo tambem roda testes/agentes re-identificados, e uma
// referencia de outro agentId faria o primeiro ciclo disparar um scan extra.
var (
	wuPollMu           sync.Mutex
	wuLastSign         string
	wuLastSignAgentID  string
	wuLastSignAt       time.Time
	wuLastPollAt       time.Time
	wuLastChangeScanAt time.Time
	// wuLastFullScanAt: ultimo scan COMPLETO (periodico, comando ou por
	// mudanca). Base da janela de supressao durante download.
	wuLastFullScanAt time.Time
	// wuLastStableSign: assinatura SEM os campos volateis de download. Serve
	// para diferenciar "mudou algo relevante" (reboot/servico/KB instalada) de
	// "so o progresso do download mudou" — o piso de 30 min vale so para o
	// segundo caso.
	wuLastStableSign string
	wuLastPollInfo   WindowsUpdatePollResult
	// wuDownloadSuppressLogged evita repetir o mesmo log a cada ciclo de 5 min
	// enquanto o download estiver em andamento.
	wuDownloadSuppressLogged bool
)

// windowsUpdateDownloadScanMinGap e o piso entre scans por mudanca enquanto ha
// download em andamento: o Windows reescreve a porcentagem a cada minuto, o que
// muda a assinatura do estado o tempo todo e fazia um scan WUA (~15s) a cada
// 2 min. 30 min mantem o dado atual sem transformar o download em rajada.
// O gap minimo do chamador (windowsUpdateChangeScanMinGap) continua valendo
// como piso quando for MAIOR que este valor.
const windowsUpdateDownloadScanMinGap = 30 * time.Minute

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
	stableSignature := windowsupdate.WindowsUpdateStateStableSignature(state)

	agentID := strings.TrimSpace(s.debugConfig().AgentID)
	wuPollMu.Lock()
	previous := wuLastSign
	// Referencia de OUTRO agente (processo re-registrado) nao vale.
	if wuLastSignAgentID != "" && agentID != "" && wuLastSignAgentID != agentID {
		previous = ""
		wuLastChangeScanAt = time.Time{}
	}
	first := previous == ""
	changed := previous != signature
	// Mudanca RELEVANTE: a assinatura estavel (sem download/progresso) mudou.
	// So nesse caso o gap normal do chamador vale; o piso maior de download
	// existe para o "vai e vem" do progresso.
	stableChanged := first || wuLastStableSign != stableSignature
	lastScanAt := wuLastChangeScanAt
	if wuLastFullScanAt.After(lastScanAt) {
		lastScanAt = wuLastFullScanAt
	}
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
		wuDownloadSuppressLogged = false
		wuLastPollInfo = result
		wuPollMu.Unlock()
		return result
	}

	s.logf(fmt.Sprintf("[windows-update] sonda de estado%s: assinatura mudou (%s -> %s) service=%s reboot=%t(%v) download=%t lastInstallSuccess=%s",
		firstLabel(first), shortSignature(previous), shortSignature(signature),
		state.ServiceState, state.RebootPending, state.RebootReasons, state.DownloadInProgress,
		formatStateTime(state.LastInstallSuccess)))

	// Piso entre scans por mudanca. Durante download o piso sobe para
	// windowsUpdateDownloadScanMinGap SÓ quando o que mudou foi o proprio
	// download (progresso/instalação em andamento). Mudanca de reboot, serviço
	// ou contagem de KBs mantém o gap normal — antes, um reboot pendente que
	// aparecia no meio de um download ficava 30 min invisível na UI.
	effectiveGap := minScanGap
	downloadingTrim := false
	if state.DownloadInProgress && !stableChanged && effectiveGap < windowsUpdateDownloadScanMinGap {
		effectiveGap = windowsUpdateDownloadScanMinGap
		downloadingTrim = true
	}
	if effectiveGap > 0 && !lastScanAt.IsZero() && time.Since(lastScanAt) < effectiveGap {
		// Evita scan em rajada; a referencia NAO avanca para que a mudanca seja
		// vista no proximo ciclo.
		message := fmt.Sprintf("[windows-update] scan sob demanda adiado: ultimo scan ha %s (< %s)",
			time.Since(lastScanAt).Round(time.Second), effectiveGap)
		if downloadingTrim {
			message = fmt.Sprintf("[windows-update] scan sob demanda SUPRIMIDO: download em andamento (progresso=%d%%) e ultimo scan ha %s (< %s)",
				state.DownloadProgressPercent, time.Since(lastScanAt).Round(time.Second), effectiveGap)
		}
		wuPollMu.Lock()
		// Loga uma vez por janela: sem isso a sonda de 5 min repetiria a mesma
		// linha enquanto o download durasse.
		shouldLog := !downloadingTrim || !wuDownloadSuppressLogged
		if downloadingTrim {
			wuDownloadSuppressLogged = true
		}
		wuLastPollInfo = result
		wuPollMu.Unlock()
		if shouldLog {
			s.logf(message)
		}
		return result
	}

	scanAt := time.Now()
	wuPollMu.Lock()
	wuDownloadSuppressLogged = false
	wuLastChangeScanAt = scanAt
	wuLastFullScanAt = scanAt
	// A referencia avanca JA: se o scan falhar, a proxima mudanca real volta a
	// disparar (a assinatura de falha inclui serviceState/policyError).
	wuLastSign = signature
	wuLastStableSign = stableSignature
	wuLastSignAgentID = agentID
	wuLastSignAt = scanAt
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
	s.SetWindowsUpdateBaselineWithStable(signature, "")
}

// SetWindowsUpdateBaselineWithStable e a forma completa: alem da assinatura
// normal (que inclui download/progresso), fixa a assinatura ESTAVEL usada para
// decidir o piso de scan durante download. Sem ela, o primeiro ciclo depois de
// um scan periódico trataria "só progresso mudou" como mudança relevante.
func (s *Service) SetWindowsUpdateBaselineWithStable(signature, stableSignature string) {
	if strings.TrimSpace(signature) == "" {
		return
	}
	agentID := ""
	if s != nil {
		agentID = strings.TrimSpace(s.debugConfig().AgentID)
	}
	wuPollMu.Lock()
	wuLastSign = signature
	wuLastSignAgentID = agentID
	wuLastSignAt = time.Now()
	// Um scan periodico/comando acabou de enviar o estado: ele tambem conta
	// como "ultimo scan completo" para a janela de supressao durante download.
	wuLastFullScanAt = wuLastSignAt
	wuLastStableSign = stableSignature
	wuDownloadSuppressLogged = false
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
