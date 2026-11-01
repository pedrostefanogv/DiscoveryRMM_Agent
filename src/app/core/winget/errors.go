package winget

import (
	"regexp"
	"strings"
)

// friendlyWingetErrors mapeia códigos de saída do winget observados em campo
// para mensagens amigáveis (pt-BR) exibidas nas notificações da loja.
//
// O texto cru do winget ("exit status 0x8a150011") não diz nada ao usuário
// final: a mensagem traduzida evita abertura de chamado para falhas que são
// transitórias ou do próprio catálogo winget (ex.: instalador rolling cujo
// hash ainda não foi atualizado no repositório).
var friendlyWingetErrors = map[string]string{
	// APPINSTALLER_CLI_ERROR_INSTALLER_HASH_MISMATCH — o instalador baixado da
	// URL oficial não bate com o SHA256 do manifesto. Típico de instaladores
	// "rolling" (ex.: googlechromestandaloneenterprise64.msi é sempre o latest):
	// o Google publica um build novo e o manifesto do winget fica defasado
	// até o repositório atualizar. Como admin/SYSTEM o winget RECUSA ignorar
	// o hash ("cannot be overridden when running as admin"), então não há
	// flag que contorne — só aguardar a atualização do catálogo.
	"0x8a150011": "Hash do instalador não confere com o catálogo (catálogo winget provavelmente desatualizado). Tente novamente mais tarde.",

	// APPINSTALLER_CLI_ERROR_NO_APPLICABLE_INSTALLER — nenhum instalador do
	// manifesto se aplica ao escopo/contexto pedido (ex.: --scope machine em
	// pacote user-only).
	"0x8a150106": "Nenhum instalador aplicável a este escopo no catálogo do pacote.",

	// APPINSTALLER_CLI_ERROR_NO_APPLICABLE_UPDATE — não há o que atualizar.
	"0x8a15002b": "Nenhuma atualização aplicável para este pacote agora.",

	// APPINSTALLER_CLI_ERROR_INTERNAL_ERROR — falha interna do winget,
	// observada em campo com execuções concorrentes (install + preload).
	"0x8a150001": "Falha interna do winget. Tente novamente em alguns minutos.",

	// APPINSTALLER_CLI_ERROR_INVALID_ARGS / arguments for installer invalid —
	// switches do catálogo incompatíveis com o instalador do pacote.
	"0x8a15004a": "Argumentos de instalação inválidos para o instalador deste pacote.",

	// APPINSTALLER_CLI_ERROR_PROMPTINPUTERROR — sem terminal para aceitar os
	// termos (contexto de serviço sem --accept-*-agreements).
	"0x8a150042": "O winget não conseguiu confirmar os termos da fonte (execução sem terminal).",
}

// Marcadores de texto do output do winget que identificam a mesma falha mesmo
// quando o código não aparece no erro do processo (o winget às vezes imprime o
// motivo no stdout e sai com outro código genérico).
var friendlyWingetTextMarkers = map[string]string{
	"installer hash does not match": "Hash do instalador não confere com o catálogo (catálogo winget provavelmente desatualizado). Tente novamente mais tarde.",
	"no applicable installer":       "Nenhum instalador aplicável a este escopo no catálogo do pacote.",
}

// wingetCodePattern extrai códigos hexadecimais do espaço winget
// (0x8A15xxxx, APPINSTALLER_CLI_ERROR_*) de textos de erro/saída.
var wingetCodePattern = regexp.MustCompile(`(?i)0x8a1[0-9a-f]{5}`)

// FriendlyFailure procura, em texto de erro e/ou saída do winget, um código
// conhecido (0x8A15xxxx) ou marcador de falha e devolve (código, mensagem
// amigável). ok=false quando nada é reconhecido — o caller mantém a mensagem
// genérica atual.
//
// A ordem de busca é: códigos explícitos primeiro (são inequívocos), depois
// marcadores de texto do output.
func FriendlyFailure(texts ...string) (code, message string, ok bool) {
	joined := strings.Join(texts, "\n")

	if m := wingetCodePattern.FindString(joined); m != "" {
		code = strings.ToLower(m)
		if msg, found := friendlyWingetErrors[code]; found {
			return code, msg, true
		}
		// Código do espaço winget não mapeado: devolve o código sem mensagem
		// para que o caller ao menos possa catalogá-lo.
		return code, "", false
	}

	lower := strings.ToLower(joined)
	for marker, msg := range friendlyWingetTextMarkers {
		if strings.Contains(lower, marker) {
			return "", msg, true
		}
	}
	return "", "", false
}
