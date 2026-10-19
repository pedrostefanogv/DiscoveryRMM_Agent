package remotesession

import "testing"

// O replay do worker (watchdog de crash/logoff) não pode reabrir o prompt de
// uma sessão já autorizada nem "ressuscitar" sessão negada/encerrada.
func TestClassifyRespawnSessionStatus(t *testing.T) {
	cases := map[string]respawnConsentAction{
		"active":          respawnConsentSkip,
		"ACTIVE":          respawnConsentSkip,
		" active ":        respawnConsentSkip,
		"pending_consent": respawnConsentPrompt,
		"Pending_Consent": respawnConsentPrompt,
		"denied":          respawnConsentRefuse,
		"closed":          respawnConsentRefuse,
		"expired":         respawnConsentRefuse,
		"":                respawnConsentPrompt, // desconhecido → prompt (fail-safe)
		"weird-status":    respawnConsentPrompt,
	}
	for status, want := range cases {
		if got := classifyRespawnSessionStatus(status); got != want {
			t.Errorf("classifyRespawnSessionStatus(%q) = %v, want %v", status, got, want)
		}
	}
}
