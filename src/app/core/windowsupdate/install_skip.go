package windowsupdate

// WU-07: semantica de "nao encontrado" e verificacao pos-instalacao.
//
// Antes: qualquer alvo ausente da busca fresca virava NotFound e derrubava o
// lote (exit code 1), mesmo quando o update ja estava instalado ou simplesmente
// saiu do catalogo (superseded/expirado) entre a aprovacao e a execucao. Isso
// gerava "falha" falsa no painel e no alerta.
//
// Regra (portada do Breeze):
//  • ausente, mas presente na busca de instalados -> SKIP already_installed;
//  • ausente e a busca fresca teve itens ILEGIVEIS -> FALHA (nao podemos
//    afirmar que o update nao foi oferecido; esconder isso seria pior);
//  • ausente e a busca foi integralmente lida -> SKIP not_offered.

// ClassifyMissingTarget decide o que fazer com um alvo que nao apareceu na
// busca de pendentes.
func ClassifyMissingTarget(updateKey string, installedKeys map[string]bool, unreadable int) (InstallOutcome, bool) {
	outcome := InstallOutcome{UpdateKey: updateKey}
	if installedKeys[updateKey] {
		outcome.SkipReason = InstallSkipAlreadyInstalled
		return outcome, false
	}
	if unreadable > 0 {
		outcome.Error = "update nao encontrado e a busca teve itens ilegiveis (enumeracao incompleta)"
		return outcome, true
	}
	outcome.SkipReason = InstallSkipNotOffered
	return outcome, false
}

// StillPendingAfterInstall devolve os alvos que continuam aparecendo como
// pendentes depois de a instalacao reportar sucesso. Quando isso acontece o
// update foi aceito mas ainda nao efetivado: o reinicio e necessario para
// concluir (verificacao pos-instalacao).
func StillPendingAfterInstall(installed []InstallOutcome, pendingKeys map[string]bool) []string {
	still := make([]string, 0, len(installed))
	for _, outcome := range installed {
		if pendingKeys[outcome.UpdateKey] {
			still = append(still, outcome.UpdateKey)
		}
	}
	return still
}
