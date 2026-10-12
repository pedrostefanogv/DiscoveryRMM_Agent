package automation

import "testing"

func TestExpandCustomFieldTokens(t *testing.T) {
	fields := map[string]any{
		"anydesk": "123 456 789",
		"ativo":   true,
		"inativo": false,
		"ram":     float64(16),
		"tags":    []any{"a", "b"},
	}

	cases := []struct {
		name   string
		input  string
		values map[string]any
		want   string
	}{
		{"substitui chave conhecida", "ID: [[campo:anydesk]]", fields, "ID: 123 456 789"},
		{"aceita espaços na chave", "[[campo: anydesk ]]", fields, "123 456 789"},
		{"booleano verdadeiro", "[[campo:ativo]]", fields, "true"},
		{"booleano falso", "[[campo:inativo]]", fields, "false"},
		{"número inteiro sem casa decimal", "[[campo:ram]]", fields, "16"},
		{"lista separada por vírgula", "[[campo:tags]]", fields, "a, b"},
		{"vários tokens no mesmo texto", "A=[[campo:anydesk]] B=[[campo:ram]]", fields, "A=123 456 789 B=16"},
		{"mantém token de chave desconhecida", "x [[campo:inexistente]] y", fields, "x [[campo:inexistente]] y"},
		{"token sem fechamento fica intacto", "x [[campo:anydesk", fields, "x [[campo:anydesk"},
		{"texto sem token não muda", "ls -la /tmp", fields, "ls -la /tmp"},
		{"sem campos, nada é substituído", "[[campo:anydesk]]", nil, "[[campo:anydesk]]"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := expandCustomFieldTokens(testCase.input, testCase.values)
			if got != testCase.want {
				t.Fatalf("expandCustomFieldTokens(%q) = %q; esperado %q", testCase.input, got, testCase.want)
			}
		})
	}
}
