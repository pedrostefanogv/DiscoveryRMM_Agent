package windowsupdate

// Assinatura do estado local do Windows Update.
//
// A assinatura e o mecanismo que evita a espera de ate N horas por um scan
// novo: a sonda barata roda a cada poucos minutos e, quando a assinatura MUDA
// (download concluido, reboot pendente, KB instalada, servico caiu), o agente
// dispara um scan completo do WUA + upload imediatamente.
//
// Regras do formato (contrato interno, estavel entre versoes do agente):
//   - somente o que muda de verdade entra (nada de timestamps de sonda);
//   - listas de motivos ja vem ordenadas;
//   - a assinatura e uma sequencia curta de "chave=valor" separada por "|".

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

// StateSignatureParts devolve a forma textual canonica do estado (sem hash).
// Exposta para testes/diagnostico: nunca inclui CheckedAt nem campos que mudam
// a cada sonda sem representar mudanca real.
func StateSignatureParts(state WindowsUpdateState) []string {
	reasons := append([]string(nil), state.RebootReasons...)
	sort.Strings(reasons)

	qfes := append([]string(nil), state.RecentQFEs...)
	sort.Strings(qfes)

	parts := []string{
		"supported=" + boolToken(state.Supported),
		"service=" + strings.TrimSpace(state.ServiceState),
		"online=" + boolToken(state.WuOnline),
		"policyManaged=" + boolToken(state.PolicyManaged),
		"wsus=" + strings.TrimSpace(state.WSUSServer),
		"reboot=" + boolToken(state.RebootPending),
		"reasons=" + strings.Join(reasons, ","),
		"lastInstallSuccess=" + formatStateTime(state.LastInstallSuccess),
		"lastInstall=" + formatStateTime(state.LastInstallTime),
		"downloadInProgress=" + boolToken(state.DownloadInProgress),
		"qfeCount=" + strconv.Itoa(state.QFEInstalledCount),
		"recentQfes=" + strings.Join(qfes, ","),
		"auOptions=" + strconv.Itoa(state.AuOptions),
		"noAutoUpdate=" + boolToken(state.NoAutoUpdate),
		"deferralDays=" + strconv.Itoa(state.DeferralDays),
		"activeHours=" + formatOptionalInt(state.ActiveHoursStart) + "-" + formatOptionalInt(state.ActiveHoursEnd),
		"policyError=" + strings.TrimSpace(state.PolicyErrorCode),
	}
	return parts
}

// WindowsUpdateStateSignature devolve o hash estavel da assinatura do estado.
// Estados equivalentes (mesma ordem de motivos, mesmo instante) produzem o
// mesmo hash; CheckedAt nao participa.
func WindowsUpdateStateSignature(state WindowsUpdateState) string {
	canonical := strings.Join(StateSignatureParts(state), "|")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// SameWindowsUpdateState informa se dois estados sao equivalentes para a
// assinatura (ignora CheckedAt).
func SameWindowsUpdateState(a, b WindowsUpdateState) bool {
	return WindowsUpdateStateSignature(a) == WindowsUpdateStateSignature(b)
}

func boolToken(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// formatStateTime usa resolucao de segundo: o WUClient grava timestamps com
// precisao de segundo/minuto e subsegundos so criariam ruido na assinatura.
func formatStateTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	return strconv.FormatInt(value.UTC().Unix(), 10)
}

func formatOptionalInt(value *int) string {
	if value == nil {
		return "-"
	}
	return strconv.Itoa(*value)
}
