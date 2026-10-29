package thirdparty

import (
	"strings"
)

// ParseWingetUpgradeTable parseia a saida TABELA de "winget upgrade".
//
// Descoberto em campo (2026-10-29): o winget 1.30 responde
// "O nome do argumento não foi reconhecido ... '--output'", ou seja, nem toda
// versão suporta JSON — sem tabela o provider ficaria inútil em frota real.
//
// A tabela é LOCALIZADA (pt-BR: Nome/ID/Versão/Disponível/Origem), as colunas
// têm largura fixa por tabela e o CABEÇALHO recebe o mesmo padding dos dados.
// Duas armadilhas resolvidas aqui:
//  1. posições de coluna são derivadas do cabeçalho;
//  2. o alinhamento é por CARACTERE (runa), não por byte: "Versão" tem ã
//     multibyte e fatiar em bytes desloca as colunas em 1–2 posições.
//
// Linhas com ID truncado (winget corta com "…") são descartadas: não é possível
// atualizar por um ID incompleto.
func ParseWingetUpgradeTable(raw string) []AppUpdate {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")

	headerIndex := -1
	var starts []int
	for i, line := range lines {
		candidate := strings.TrimRight(line, " ")
		if candidate == "" {
			continue
		}
		runes := []rune(candidate)
		if !isWingetTableHeader(runes) {
			continue
		}
		columns := headerColumns(runes)
		if len(columns) < 3 {
			continue
		}
		headerIndex = i
		starts = columns
		break
	}
	if headerIndex < 0 {
		return nil
	}

	headerNames := sliceColumns([]rune(strings.TrimRight(lines[headerIndex], " ")), starts)
	idCol, ok := pickColumn(headerNames, isIDHeader)
	if !ok {
		return nil
	}
	versionCol, _ := pickColumn(headerNames, isVersionHeader)
	availableCol, _ := pickColumn(headerNames, isAvailableHeader)
	sourceCol, _ := pickColumn(headerNames, isSourceHeader)

	updates := make([]AppUpdate, 0, 8)
	for _, line := range lines[headerIndex+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isSeparatorLine(trimmed) || isFooterLine(trimmed) {
			continue
		}

		fields := sliceColumns([]rune(strings.TrimRight(line, " ")), starts)
		id := fieldAt(fields, idCol)
		if id == "" || strings.HasSuffix(id, "…") || strings.HasSuffix(id, "...") {
			continue // ID truncado pela tabela: não é atualizável com segurança
		}
		if !ValidWingetID(id) {
			continue
		}

		installed := fieldAt(fields, versionCol)
		available := fieldAt(fields, availableCol)
		if available == "" || strings.EqualFold(available, installed) {
			continue
		}

		name := ""
		if len(fields) > 0 {
			name = fields[0]
		}
		source := fieldAt(fields, sourceCol)
		if source == "" {
			source = SourceWinget
		}

		updates = append(updates, AppUpdate{
			Source:           source,
			ID:               id,
			Name:             name,
			InstalledVersion: installed,
			AvailableVersion: available,
		})
	}
	return updates
}

// headerColumns devolve os índices (em runas) onde começa cada coluna do
// cabeçalho, separando por runs de 2+ espaços.
func headerColumns(header []rune) []int {
	starts := make([]int, 0, 6)
	inColumn := false
	spaces := 0
	for i := 0; i < len(header); i++ {
		if header[i] == ' ' {
			spaces++
			continue
		}
		if !inColumn {
			starts = append(starts, i)
			inColumn = true
			spaces = 0
			continue
		}
		if spaces >= 2 {
			starts = append(starts, i)
		}
		spaces = 0
	}
	return starts
}

// sliceColumns fatia a linha (em runas) nas colunas indicadas por starts.
func sliceColumns(line []rune, starts []int) []string {
	fields := make([]string, len(starts))
	for i := range starts {
		start := starts[i]
		end := len(line)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		if start >= len(line) {
			fields[i] = ""
			continue
		}
		if end > len(line) {
			end = len(line)
		}
		if end <= start {
			fields[i] = ""
			continue
		}
		fields[i] = strings.TrimSpace(string(line[start:end]))
	}
	return fields
}

func fieldAt(fields []string, index int) string {
	if index < 0 || index >= len(fields) {
		return ""
	}
	return fields[index]
}

func pickColumn(names []string, match func(string) bool) (int, bool) {
	for i, name := range names {
		if match(name) {
			return i, true
		}
	}
	return -1, false
}

func isIDHeader(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized == "id" || normalized == "identificador"
}

func isVersionHeader(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(normalized, "vers") || normalized == "version"
}

func isAvailableHeader(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(normalized, "dispon") || strings.HasPrefix(normalized, "available")
}

func isSourceHeader(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(normalized, "origem") || normalized == "source"
}

// isWingetTableHeader reconhece o cabeçalho em pt-BR e inglês.
func isWingetTableHeader(header []rune) bool {
	names := sliceColumns(header, headerColumns(header))
	if len(names) < 3 {
		return false
	}
	hasID := false
	hasOther := false
	for _, name := range names {
		switch {
		case isIDHeader(name):
			hasID = true
		case isVersionHeader(name), isAvailableHeader(name), isSourceHeader(name),
			strings.EqualFold(strings.TrimSpace(name), "nome"),
			strings.EqualFold(strings.TrimSpace(name), "name"):
			hasOther = true
		}
	}
	return hasID && hasOther
}

func isSeparatorLine(line string) bool {
	if line == "" {
		return false
	}
	for _, r := range line {
		if r != '-' && r != '=' && r != ' ' {
			return false
		}
	}
	return true
}

func isFooterLine(line string) bool {
	normalized := strings.ToLower(line)
	for _, marker := range []string{
		"atualizaç", "atualizac", "updates available", "upgrade available", "package(s) available",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
