package p2p

import (
	"fmt"
	"strings"
	"time"
)

// artifactRetentionChecker é injetado pelo App: retorna true quando o artifact
// pode ser REMOVIDO do cache local por o pacote já estar em estado final
// (instalado/sem update pendente). Sem hook, nada é removido.
var artifactRetentionChecker func(artifactID, artifactName string) bool

// SetArtifactRetentionChecker registra o verificador de retenção.
func SetArtifactRetentionChecker(checker func(artifactID, artifactName string) bool) {
	artifactRetentionChecker = checker
}

// PruneFinalStateArtifacts remove do P2P_Temp instaladores de pacotes que já
// estão em estado final: não serão mais usados pela automação e ocupam centenas
// de MB (ex.: Chrome 520 MB). Respeita transferências em andamento (serving
// sessions) para não quebrar um download de peer.
func (c *Coordinator) PruneFinalStateArtifacts(now time.Time) (int, error) {
	_ = now
	if c == nil || c.deps == nil || artifactRetentionChecker == nil {
		return 0, nil
	}
	artifacts, err := c.ListArtifacts()
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, artifact := range artifacts {
		name := strings.TrimSpace(artifact.ArtifactName)
		if name == "" {
			continue
		}
		if !artifactRetentionChecker(artifact.ArtifactID, name) {
			continue
		}
		if c.artifactBeingServed(name) {
			continue // peer baixando agora: remover quebraria a transferência
		}
		if err := c.DeleteArtifact(name); err != nil {
			continue
		}
		c.deps.Log(fmt.Sprintf("[p2p][retention] artifact removido (pacote em estado final) id=%s name=%s size=%d", artifact.ArtifactID, name, artifact.SizeBytes))
		removed++
	}
	return removed, nil
}

// DeleteArtifactAfterInstall remove do P2P_Temp o instalador de um pacote cuja
// instalação acabou de ser concluída com sucesso (liberação imediata de disco em
// vez de esperar o tick de retenção de 1h). Respeita transferências em andamento
// para não quebrar o download de um peer.
func (c *Coordinator) DeleteArtifactAfterInstall(artifactName string) bool {
	name := strings.TrimSpace(artifactName)
	if c == nil || name == "" {
		return false
	}
	if c.artifactBeingServed(name) {
		return false
	}
	if err := c.DeleteArtifact(name); err != nil {
		if c.deps != nil {
			c.deps.Log(fmt.Sprintf("[p2p][retention] falha ao remover artifact pos-instalacao name=%s err=%v", name, err))
		}
		return false
	}
	if c.deps != nil {
		c.deps.Log(fmt.Sprintf("[p2p][retention] artifact removido pos-instalacao name=%s", name))
	}
	return true
}

// artifactBeingServed informa se há uma serving session ativa para o artifact.
func (c *Coordinator) artifactBeingServed(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	c.servingSessionsMu.Lock()
	defer c.servingSessionsMu.Unlock()
	prefix := name + "|"
	for key := range c.servingSessions {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
