package windowsupdate

// Normalizacao pura das linhas do Win32_QuickFixEngineering (QFE).
//
// Contexto do bug (maquina 019faa6f): oleutil.ForEach entrega VARIANTs
// EMPRESTADOS pela enumeracao (o VARIANT nao e liberado por nos) e
// VARIANT.ToString() usa SysStringLen, que depende do cabecalho do BSTR. Com o
// VARIANT aposentado/realocado a leitura devolvia 0 e TODA string virava "",
// produzindo titulos como "Atualizacao KB5127839" e descricoes vazias mesmo
// com Caption preenchido no WMI. A extracao passou a usar o ponteiro do BSTR
// direto da VARIANT (ver variantGoString em qfe_windows.go); a formatacao do
// titulo/descricao vive aqui para ser testavel sem Windows.

import (
	"regexp"
	"strings"
)

var (
	// KB article URLs usadas pelo WMI como "Caption" no Windows 11/Server
	// recentes: "https://support.microsoft.com/help/5127690" ou
	// "http://support.microsoft.com/?kbid=5127690".
	qfeCaptionURLPattern = regexp.MustCompile(`(?i)^https?://[^/]+/(?:help/|\?kbid=|kb/)?(\d{4,10})/?$`)
	qfeCaptionKBPattern  = regexp.MustCompile(`(?i)^(?:atualizacao|update)?\s*(?:para\s+)?(?:o\s+|a\s+)?(?:windows)?[\s-]*(KB\d{4,10})$`)
	qfeKBTokenPattern    = regexp.MustCompile(`(?i)^KB\d{4,10}$`)
	qfeSpacePattern      = regexp.MustCompile(`\s+`)
)

// QFETitleInfo e o titulo/descricao normalizados de uma KB instalada.
type QFETitleInfo struct {
	Title       string
	Description string
	// Placeholder=true quando o WMI nao trouxe caption real e o titulo foi
	// sintetizado a partir do KB (nunca deve ser tratado como nome "bonito").
	Placeholder bool
}

// NormalizeQFETitle monta o titulo exibivel de uma KB instalada.
//
// Regras:
//   - Caption real (nao vazio, nao uma URL de suporte, nao apenas "KBxxxx")
//     e usado como titulo;
//   - Caption que e URL do tipo support.microsoft.com/help/<kb> e descartado
//     (e o mesmo dado que ja temos no KBID) e o titulo vira
//     "Atualizacao <KB>" com Placeholder=true;
//   - Diferenca de caixa ("Atualizacao KB..." vs "Atualizacao kb...") nao
//     duplica: normalizamos o prefixo para "Atualizacao KB".
func NormalizeQFETitle(kb, caption, description string) QFETitleInfo {
	kbID := NormalizeKB(kb)
	cleanCaption := collapseAndTrim(caption)
	cleanDescription := collapseAndTrim(description)

	title := ""
	placeholder := false
	switch {
	case cleanCaption == "":
		placeholder = true
	case isQFEPlaceholderCaption(cleanCaption, kbID):
		placeholder = true
	case qfeCaptionURLPattern.MatchString(cleanCaption):
		placeholder = true
	default:
		title = cleanCaption
	}

	if title == "" {
		if kbID != "" {
			title = "Atualizacao " + kbID
		} else {
			title = "Atualizacao do Windows"
		}
	}

	// A descricao do WMI em muitas maquinas repete "http://support.microsoft.com/?kbid=..." —
	// mantemos o texto, mas nunca como titulo.
	return QFETitleInfo{
		Title:       truncate(title, 500),
		Description: truncate(cleanDescription, 2000),
		Placeholder: placeholder,
	}
}

// isQFEPlaceholderCaption detecta captions que nao identificam a atualizacao:
// apenas o numero do KB (com ou sem prefixo "Atualizacao"/"Update").
func isQFEPlaceholderCaption(caption, kbID string) bool {
	match := qfeCaptionKBPattern.FindStringSubmatch(caption)
	if match == nil {
		return false
	}
	if kbID == "" {
		return true
	}
	return strings.EqualFold(NormalizeKB(match[1]), kbID)
}

// IsQFEURLCaption informa se o caption e apenas uma URL de suporte da KB.
func IsQFEURLCaption(caption string) bool {
	return qfeCaptionURLPattern.MatchString(collapseAndTrim(caption))
}

// IsKBPrefixedTitle informa se o texto comeca pelo numero de uma KB
// (ex.: "KB5127690"). Usado para nao montar "Atualizacao Atualizacao KB..." .
func IsKBPrefixedTitle(title string) bool {
	trimmed := collapseAndTrim(title)
	if trimmed == "" {
		return false
	}
	firstField := trimmed
	if idx := strings.IndexAny(trimmed, " \t"); idx > 0 {
		firstField = trimmed[:idx]
	}
	return qfeKBTokenPattern.MatchString(firstField)
}

func collapseAndTrim(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(trimmed)
	return strings.TrimSpace(qfeSpacePattern.ReplaceAllString(trimmed, " "))
}
