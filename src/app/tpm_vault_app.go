package app

import "discovery/app/core/tpmvault"

// GetTpmVaultStatus devolve o retrato MASCARADO do cofre TPM de credenciais
// (política efetiva, disponibilidade, host, prefixo do token, último erro).
//
// Nunca devolve o token: o status é exposto ao frontend via Wails, ao HTTP de
// debug (/api/GetTpmVaultStatus) e aos bridges da UI companion.
func (a *App) GetTpmVaultStatus() tpmvault.Status {
	if a == nil || a.DebugSvc == nil {
		return tpmvault.Status{LastError: "servico de debug indisponivel"}
	}
	return a.DebugSvc.GetTPMVaultStatus()
}

// EraseTpmVault apaga o registro do cofre TPM (pedido explícito de operador).
//
// NÃO é exposto via HTTP de debug: está em debugBlockedReflectionMethods, porque
// apagar o cofre é destrutivo e não pode ser disparado por um simples request
// autenticado pelo token de debug. O uso legítimo é o comando remoto de
// descomissionamento e a flag CLI --tpm-vault-clear (elevada).
func (a *App) EraseTpmVault(reason string) error {
	if a == nil || a.DebugSvc == nil {
		return nil
	}
	return a.DebugSvc.EraseTPMVault(reason)
}
