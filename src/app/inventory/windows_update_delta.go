package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"discovery/app/core/windowsupdate"
)

// WU-13: delta de inventario de Windows Update.
//
// O agente ja suprimia uploads quando NADA mudava (hash do payload). O que
// faltava era reduzir o payload quando MUDA POUCO: num scan disparado por
// mudanca (trigger=change) o normal e um item novo aparecer ou um download
// comecar — e reenviar 1000 updates por isso custa banda e tempo de banco.
//
// O delta so e usado quando:
//  - o trigger e "change" (periodic/command/install sempre mandam o inventario
//    inteiro, que e a referencia);
//  - existe payload anterior cacheado (sem referencia nao ha diff);
//  - a mudanca e MINORITARIA (< 50% dos itens). Acima disso o payload delta
//    fica maior que o full e nao vale o risco.
//
// O payload delta leva os updates alterados + removedKeys, e o servidor aplica
// por cima do inventario que ele ja tem. O cache local SEMPRE guarda o payload
// completo: e ele que serve de referencia para o proximo diff.

const (
	// windowsUpdateEnvelopeVersion e a versao do contrato do envelope. Vai SEMPRE
	// no payload: a API usa para decidir se entende delta/campos novos.
	windowsUpdateEnvelopeVersion = 2
	// windowsUpdateDeltaThresholdDivisor: acima dessa fracao de itens alterados
	// o payload completo e preferivel ao delta.
	windowsUpdateDeltaThresholdDivisor = 2
)

// windowsUpdateCapabilities lista o que este agente sabe enviar.
var windowsUpdateCapabilities = []string{"delta_inventory", "severity_provenance", "update_type"}

// DeltaPlan e o resultado do planejamento do delta.
type DeltaPlan struct {
	// UseDelta=false significa "mande o inventario completo".
	UseDelta bool
	// Changed sao os updates novos ou com conteudo diferente do cache.
	Changed []windowsupdate.Update
	// Removed sao chaves que existiam no cache e nao aparecem mais.
	Removed []string
}

// PlanWindowsUpdateDelta compara o resultado atual com o payload anterior.
//
// previousJSON e o envelope COMPLETO salvo no cache local. Qualquer falha de
// parse devolve o caminho conservador (full).
func PlanWindowsUpdateDelta(current []windowsupdate.Update, previousJSON []byte, trigger string) DeltaPlan {
	if len(previousJSON) == 0 {
		return DeltaPlan{}
	}
	if trigger != "change" {
		return DeltaPlan{}
	}

	var previous agentWindowsUpdateEnvelope
	if err := json.Unmarshal(previousJSON, &previous); err != nil {
		return DeltaPlan{}
	}

	previousHash := make(map[string]string, len(previous.Updates))
	previousKeys := make(map[string]bool, len(previous.Updates))
	for _, update := range previous.Updates {
		if update.UpdateKey == "" {
			continue
		}
		previousKeys[update.UpdateKey] = true
		previousHash[update.UpdateKey] = updateContentHash(update)
	}

	currentKeys := make(map[string]bool, len(current))
	changed := make([]windowsupdate.Update, 0, len(current))
	for _, update := range current {
		if update.UpdateKey == "" {
			continue
		}
		currentKeys[update.UpdateKey] = true
		if hash, ok := previousHash[update.UpdateKey]; !ok || hash != updateContentHash(update) {
			changed = append(changed, update)
		}
	}

	removed := make([]string, 0, 4)
	for key := range previousKeys {
		if !currentKeys[key] {
			removed = append(removed, key)
		}
	}

	// Lista atual vazia: nao ha "delta" a mandar (o inventario completo vazio
	// tem semantica propria no servidor — preservar o ultimo bom).
	if len(current) == 0 {
		return DeltaPlan{}
	}

	// Mudanca minoritaria: delta compensa. Acima de 50% manda tudo (menos risco
	// de payload estranho e o servidor nao precisa mesclar quase tudo).
	if len(changed)*windowsUpdateDeltaThresholdDivisor > len(current) {
		return DeltaPlan{}
	}

	return DeltaPlan{UseDelta: true, Changed: changed, Removed: removed}
}

// updateContentHash e o hash do conteudo de UM update (sem depender da ordem).
func updateContentHash(update windowsupdate.Update) string {
	payload, err := json.Marshal(update)
	if err != nil {
		// Sem hash confiavel, trata como "mudou": reenviar e seguro.
		return "unhashable"
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
