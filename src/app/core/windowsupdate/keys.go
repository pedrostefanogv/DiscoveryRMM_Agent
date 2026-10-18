package windowsupdate

import (
	"fmt"
	"regexp"
	"strings"
)

// Chaves canonicas usadas no inventario e nas aprovacoes:
//
//	guid:<GUID do UpdateID>:<RevisionNumber>   (updates pendentes do WUA)
//	kb:KB5034441                               (KB instalado via QFE)
//
// A chave e o contrato com a API: o servidor aprova/instala por ela e o agente
// revalida contra o catalogo atual. Por isso a validacao aqui e estrita — nada
// de texto livre, nada de titulo.
const (
	keyPrefixGUID = "guid:"
	keyPrefixKB   = "kb:"
)

var (
	guidPattern = regexp.MustCompile("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
	kbPattern   = regexp.MustCompile("^KB[0-9]{4,10}$")
)

// NormalizeGUID normaliza um GUID do WUA para o formato canonico minusculo sem
// chaves. Retorna "" quando invalido.
func NormalizeGUID(guid string) string {
	g := strings.TrimSpace(guid)
	g = strings.TrimPrefix(g, "{")
	g = strings.TrimSuffix(g, "}")
	g = strings.ToLower(g)
	if !guidPattern.MatchString(g) {
		return ""
	}
	return g
}

// NormalizeKB normaliza um identificador de KB para "KBxxxx".
// Aceita "5034441", "kb5034441", "KB 5034441". Retorna "" quando invalido.
func NormalizeKB(kb string) string {
	raw := strings.ToUpper(strings.TrimSpace(kb))
	raw = strings.ReplaceAll(raw, " ", "")
	raw = strings.TrimPrefix(raw, "KB")
	raw = strings.TrimLeft(raw, "0")
	if raw == "" {
		return ""
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return ""
		}
	}
	candidate := "KB" + raw
	if !kbPattern.MatchString(candidate) {
		return ""
	}
	return candidate
}

// NormalizeKBIDs normaliza e deduplica uma lista de KBs.
func NormalizeKBIDs(kbIDs []string) []string {
	if len(kbIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(kbIDs))
	seen := make(map[string]struct{}, len(kbIDs))
	for _, kb := range kbIDs {
		norm := NormalizeKB(kb)
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}

// GuidKey monta a chave de um update pendente. revision <= 0 e normalizado
// para 0 (o WUA sempre informa RevisionNumber, mas o caller pode nao ter).
func GuidKey(updateID string, revision int) string {
	guid := NormalizeGUID(updateID)
	if guid == "" {
		return ""
	}
	if revision < 0 {
		revision = 0
	}
	return fmt.Sprintf("%s%s:%d", keyPrefixGUID, guid, revision)
}

// KBKey monta a chave de um KB instalado.
func KBKey(kb string) string {
	norm := NormalizeKB(kb)
	if norm == "" {
		return ""
	}
	return keyPrefixKB + norm
}

// UpdateKeyFor monta a chave canonica de um update, preferindo o GUID (unico
// por revisao) e caindo para o primeiro KB quando nao ha identidade (QFE).
func UpdateKeyFor(updateID string, revision int, kbIDs []string) string {
	if key := GuidKey(updateID, revision); key != "" {
		return key
	}
	for _, kb := range kbIDs {
		if key := KBKey(kb); key != "" {
			return key
		}
	}
	return ""
}

// ParseUpdateKey valida e decompoe uma chave canonica.
// Retorna (isGUID, guidOrKB, revision, ok).
func ParseUpdateKey(key string) (bool, string, int, bool) {
	k := strings.TrimSpace(key)
	if strings.HasPrefix(k, keyPrefixGUID) {
		rest := strings.TrimPrefix(k, keyPrefixGUID)
		idx := strings.LastIndex(rest, ":")
		if idx <= 0 {
			return false, "", 0, false
		}
		guid := NormalizeGUID(rest[:idx])
		if guid == "" {
			return false, "", 0, false
		}
		var revision int
		if _, err := fmt.Sscanf(rest[idx+1:], "%d", &revision); err != nil || revision < 0 {
			return false, "", 0, false
		}
		return true, guid, revision, true
	}
	if strings.HasPrefix(k, keyPrefixKB) {
		kb := NormalizeKB(strings.TrimPrefix(k, keyPrefixKB))
		if kb == "" {
			return false, "", 0, false
		}
		return false, kb, 0, true
	}
	return false, "", 0, false
}

// ValidUpdateKey informa se a chave e uma chave canonica valida.
func ValidUpdateKey(key string) bool {
	_, _, _, ok := ParseUpdateKey(key)
	return ok
}
