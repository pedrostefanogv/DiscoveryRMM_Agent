package ai

import "discovery/app/core/buildinfo"

// codeRevision retorna o identificador curto da revisão de código que gerou o
// binário (hash do commit + "+mod" quando o worktree estava modificado) —
// delega para o pacote buildinfo, fonte única compartilhada com os demais
// logs do agente (agent.log, agent-service.log).
func codeRevision() string {
	return buildinfo.Revision()
}
