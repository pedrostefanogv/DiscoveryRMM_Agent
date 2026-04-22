package ai

import (
	"strings"
	"testing"
)

func TestBold(t *testing.T) {
	result := Bold("teste")
	expected := "**teste**"
	if result != expected {
		t.Errorf("Bold: esperado %q, obteve %q", expected, result)
	}
}

func TestItalic(t *testing.T) {
	result := Italic("teste")
	expected := "*teste*"
	if result != expected {
		t.Errorf("Italic: esperado %q, obteve %q", expected, result)
	}
}

func TestCode(t *testing.T) {
	result := Code("comando")
	expected := "`comando`"
	if result != expected {
		t.Errorf("Code: esperado %q, obteve %q", expected, result)
	}
}

func TestCodeBlock(t *testing.T) {
	code := "echo 'hello'"
	// Sem linguagem
	result := CodeBlock(code, "")
	if !strings.Contains(result, "```") {
		t.Errorf("CodeBlock sem linguagem: deveria conter ```")
	}
	if !strings.Contains(result, code) {
		t.Errorf("CodeBlock sem linguagem: deveria conter o código")
	}

	// Com linguagem
	result = CodeBlock(code, "bash")
	if !strings.Contains(result, "```bash") {
		t.Errorf("CodeBlock com linguagem: deveria conter ```bash")
	}
}

func TestWarn(t *testing.T) {
	result := Warn("perigo")
	if !strings.HasPrefix(result, "> ⚠️") {
		t.Errorf("Warn: deveria começar com '> ⚠️', obteve %q", result)
	}
}

func TestTip(t *testing.T) {
	result := Tip("dica")
	if !strings.HasPrefix(result, "> 💡") {
		t.Errorf("Tip: deveria começar com '> 💡', obteve %q", result)
	}
}

func TestNote(t *testing.T) {
	result := Note("nota")
	if !strings.HasPrefix(result, "> ℹ️") {
		t.Errorf("Note: deveria começar com '> ℹ️', obteve %q", result)
	}
}

func TestSuccess(t *testing.T) {
	result := Success("pronto")
	if !strings.HasPrefix(result, "> ✅") {
		t.Errorf("Success: deveria começar com '> ✅', obteve %q", result)
	}
}

func TestHeading(t *testing.T) {
	tests := []struct {
		level    int
		text     string
		expected string
	}{
		{1, "Título", "# Título"},
		{2, "Subtítulo", "## Subtítulo"},
		{3, "Terceiro", "### Terceiro"},
		{0, "Inválido", "# Inválido"},      // Mínimo 1
		{7, "Inválido", "###### Inválido"}, // Máximo 6
	}

	for _, tt := range tests {
		result := Heading(tt.level, tt.text)
		if result != tt.expected {
			t.Errorf("Heading(%d, %q): esperado %q, obteve %q", tt.level, tt.text, tt.expected, result)
		}
	}
}

func TestList(t *testing.T) {
	result := List("item1", "item2", "item3")
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 3 {
		t.Errorf("List: esperado 3 linhas, obteve %d", len(lines))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "- ") {
			t.Errorf("List: linha deveria começar com '- ', obteve %q", line)
		}
	}
}

func TestOrderedList(t *testing.T) {
	result := OrderedList("primeiro", "segundo", "terceiro")
	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) != 3 {
		t.Errorf("OrderedList: esperado 3 linhas, obteve %d", len(lines))
	}
	expected := []string{
		"1. primeiro",
		"2. segundo",
		"3. terceiro",
	}
	for i, line := range lines {
		if line != expected[i] {
			t.Errorf("OrderedList linha %d: esperado %q, obteve %q", i+1, expected[i], line)
		}
	}
}
