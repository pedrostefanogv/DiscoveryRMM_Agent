package inventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"discovery/app/core/thirdparty"
)

// Detecção de mudança em apps de terceiros (WU-19/WU-20).
//
// O scan completo (WUA + terceiros) roda a cada 6 h. Para um app novo aparecer
// na aba "Atualizações" em minutos — e não em até 6 h — o agente faz uma sonda
// PRÓPRIA de terceiros (3 h por padrão, jitter), compara a assinatura com o
// último inventário enviado e, se mudou, dispara scan+upload completo.
//
// A assinatura é o hash ordenado de "source:id@versãoDisponível". O baseline é
// o último payload COMPLETO salvo no cache SQLite (o mesmo usado pelo delta),
// então não há estado novo para persistir.

// thirdPartySignature calcula a assinatura canônica de um inventário de apps.
func thirdPartySignature(apps []thirdparty.AppUpdate) string {
	keys := make([]string, 0, len(apps))
	for _, app := range apps {
		source := strings.ToLower(strings.TrimSpace(app.Source))
		id := strings.TrimSpace(app.ID)
		if source == "" || id == "" {
			continue
		}
		keys = append(keys, source+":"+id+"@"+strings.TrimSpace(app.AvailableVersion))
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "|")))
	return hex.EncodeToString(sum[:])
}

// thirdPartySignatureFromEnvelope calcula a assinatura a partir do payload
// completo já enviado (baseline).
func thirdPartySignatureFromEnvelope(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	var envelope agentWindowsUpdateEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	apps := make([]thirdparty.AppUpdate, 0, len(envelope.Updates))
	for _, update := range envelope.Updates {
		if !thirdparty.IsThirdPartySource(update.Source) {
			continue
		}
		apps = append(apps, thirdparty.AppUpdate{
			Source:           update.Source,
			ID:               strings.TrimPrefix(update.UpdateKey, update.Source+":"),
			AvailableVersion: update.AvailableVersion,
		})
	}
	return thirdPartySignature(apps)
}

// ThirdPartyInventoryChanged informa se a lista de apps com atualização mudou
// desde o último upload. Sem providers instalados devolve false (nada a fazer).
func (s *Service) ThirdPartyInventoryChanged(ctx context.Context) (bool, error) {
	providers := s.thirdPartyProvidersSnapshot()
	if len(providers) == 0 {
		return false, nil
	}

	apps, _ := thirdparty.Collect(ctx, providers)
	current := thirdPartySignature(apps)
	if current == "" {
		return false, nil
	}

	_, cache, _ := s.wuDeps()
	if cache == nil {
		return false, nil
	}
	agentID := strings.TrimSpace(s.debugConfig().AgentID)
	if agentID == "" {
		return false, nil
	}
	payload, _, _, found, err := cache.GetWindowsUpdateCache(agentID)
	if err != nil || !found {
		return false, err
	}

	baseline := thirdPartySignatureFromEnvelope(payload)
	if baseline == "" {
		// Nenhum app de terceiros no último envio: qualquer inventário novo é
		// mudança (primeira coleta ou apps que passaram a existir).
		return true, nil
	}
	return baseline != current, nil
}
