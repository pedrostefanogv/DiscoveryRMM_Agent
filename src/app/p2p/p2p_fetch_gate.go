package p2p

// Gate de utilidade do fetch: decide se vale a pena baixar um artifact que a
// rede anuncia e este agent ainda não tem (re-seed/eleição de fetcher).
//
// Sem este gate, o re-seed propagava TODOS os artifacts anunciados para todos
// os agentes — incluindo instaladores de pacotes já instalados na máquina
// (ex.: Chrome 520 MB, Brave 166 MB), que ficavam em P2P_Temp sem
// necessidade. O App injeta o gate consultando o estado real do winget.
//
// Hook (não importa o pacote automation aqui para evitar ciclo de import):
// o App registra via SetArtifactFetchGate no startup.

var artifactFetchGate func(artifactID, artifactName string) bool

// SetArtifactFetchGate registra o gate de utilidade do fetch.
func SetArtifactFetchGate(gate func(artifactID, artifactName string) bool) {
	artifactFetchGate = gate
}

// fetchAllowed consulta o gate. Sem gate configurado, mantém o comportamento
// anterior (permitir) — fail-safe para testes e deployments antigos.
func fetchAllowed(artifactID, artifactName string) bool {
	if artifactFetchGate == nil {
		return true
	}
	return artifactFetchGate(artifactID, artifactName)
}
