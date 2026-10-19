package windowsupdate

// Logica PURA da deteccao de reinicio pendente.
//
// Contexto do bug (maquina 019faa6f): PendingFileRenameOperations tinha 5
// entradas "\??\C:\Program Files\Microsoft OneDrive\..." e a sonda
// devolvia Pending=false — o servidor nunca mostrava "Reinicio pendente".
// As causas tratadas aqui:
//   - GetStringsValue devolve REG_MULTI_SZ em pares (origem, destino) e o
//     destino vazio e representado como elemento vazio/ausente; entradas com
//     so o destino vazio NAO sao operacoes e nao devem gerar falso positivo;
//   - prefixos "\??\", "\SystemRoot\", "!\??\" e numeros de sessao
//     ("0", "1", ...) que alguns instaladores colocam na frente.
//
// A avaliacao e separada da leitura do registro para ser testavel em qualquer
// plataforma.

import (
	"strings"
)

// PendingFileRenameInfo resume a avaliacao de PendingFileRenameOperations.
type PendingFileRenameInfo struct {
	Pending bool
	// Operations e a quantidade de pares (origem, destino) validos.
	Operations int
	// Sources lista as origens encontradas (no maximo 10, para diagnostico).
	Sources []string
}

const pendingRenameMaxSources = 10

// EvaluatePendingFileRename decide se a lista de PendingFileRenameOperations
// indica trabalho pendente para o proximo boot.
//
// Regras:
//  1. valores vazios sozinhos nunca contam (REG_MULTI_SZ "vazio" no registro
//     aparece como um elemento "" — nao e operacao);
//  2. a lista e lida em pares: origem (elemento par) e destino (elemento
//     impar). Um par com destino vazio e valido (rename para lugar nenhum =
//     arquivo apagado no boot) e conta como operacao pendente;
//  3. elemento que sozinho ja carrega uma origem valida (\??\C:\...,
//     \SystemRoot\..., !\??\..., C:\...) conta mesmo sem par, porque
//     instaladores gravam entradas truncadas;
//  4. elemento que nao descreve um caminho (so numeros de sessao, espacos)
//     NUNCA conta — falso positivo aqui empurraria o operador a reiniciar.
func EvaluatePendingFileRename(values []string) PendingFileRenameInfo {
	info := PendingFileRenameInfo{}
	for index := 0; index < len(values); index += 2 {
		source := strings.TrimSpace(values[index])
		destination := ""
		if index+1 < len(values) {
			destination = strings.TrimSpace(values[index+1])
		}
		if source == "" && destination == "" {
			continue
		}
		if !isPendingRenameSource(source) {
			// Origem invalida (ex.: apenas numero de sessao/espacos). Entradas
			// com LIXO nao podem virar "reinicio pendente": a sonda alimenta
			// uma acao destrutiva na UI (reiniciar a maquina) e falso positivo
			// e pior que falso negativo.
			continue
		}
		info.Operations++
		if len(info.Sources) < pendingRenameMaxSources {
			info.Sources = append(info.Sources, source)
		}
	}
	info.Pending = info.Operations > 0
	return info
}

// isPendingRenameSource valida se o elemento descreve um arquivo fisico que
// sera renomeado/apagado no boot.
func isPendingRenameSource(value string) bool {
	if value == "" {
		return false
	}
	// Remove numeros de sessao que alguns instaladores usam como "slot"
	// ("0\??\C:\..."): o numero sozinho nao e caminho.
	candidate := value
	for len(candidate) > 0 && candidate[0] >= '0' && candidate[0] <= '9' {
		candidate = candidate[1:]
	}
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	// Marcador "!" do MoveFileEx (atrasa a operacao para o proximo boot) e
	// prefixo "\??\" (caminho nativo).
	candidate = strings.TrimPrefix(candidate, "!")
	candidate = strings.TrimSpace(candidate)
	candidate = strings.TrimPrefix(candidate, "\\??\\")
	candidate = strings.TrimPrefix(candidate, "\\SystemRoot\\")
	candidate = strings.TrimPrefix(candidate, "\\")
	if candidate == "" {
		return false
	}
	// Precisa parecer um caminho: letra de unidade ("C:\...") ou caminho UNC.
	if len(candidate) >= 2 && candidate[1] == ':' {
		return true
	}
	// Sem unidade explicita (raro) mas com diretorio/arquivo e aceito.
	return strings.ContainsAny(candidate, "\\/")
}

// RebootReasonsSet remonta a lista ordenada de motivos a partir de flags.
// Usado pela sonda Windows depois de cada leitura barata.
func RebootReasonsSet(reasons ...string) []string {
	seen := make(map[string]struct{}, len(reasons))
	out := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			continue
		}
		if _, ok := seen[reason]; ok {
			continue
		}
		seen[reason] = struct{}{}
		out = append(out, reason)
	}
	sortReasons(out)
	return out
}
