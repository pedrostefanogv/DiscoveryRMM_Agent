package windowsupdate

// Caminhos de registro de POLITICA do Windows Update (chaves em Policies).
//
// Sao a UNICA fonte de verdade para as chaves de politica: tanto a sonda barata
// (reboot_windows.go/state_windows.go) quanto o apply/rollback de politica
// (policy_windows.go) referenciam estas constantes. Duplicar a string em mais
// de um arquivo ja causou um bug real: policy_windows.go declarava
// "SOFTWAREPoliciesMicrosoftWindowsWindowsUpdate" (sem separador), o que fazia
// applyPolicy/restorePolicy operarem numa chave inexistente e o recurso de
// enforcement no SO nunca ter efeito.
//
// Ficam neste arquivo SEM build tag de proposito: a validacao do formato roda
// no CI Linux (policy_paths_test.go), onde o registro do Windows nao existe.
const (
	// regPathWUPolicies e a chave de politica do Windows Update (WU).
	regPathWUPolicies = `SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate`
	// regPathWUPoliciesAU e a subchave de Automatic Updates (AU).
	regPathWUPoliciesAU = regPathWUPolicies + `\AU`
)

// policyScopePaths mapeia o escopo usado no backup (AU/WU) para o caminho real
// da chave. Usado pelo rollback para reabrir a chave certa.
func policyScopePath(scope string) (string, bool) {
	switch scope {
	case policyScopeAU:
		return regPathWUPoliciesAU, true
	case policyScopeWU:
		return regPathWUPolicies, true
	default:
		return "", false
	}
}
