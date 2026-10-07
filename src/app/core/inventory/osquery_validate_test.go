package inventory

import "testing"

// O gate do osquery precisa bloquear consultas que leem arquivos ou acessam a
// rede (bypass do read_file, que exige autorizacao do usuario).
func TestValidateReadOnlyOsqueryQuery(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantErr bool
	}{
		{"select simples", "SELECT pid, name FROM processes LIMIT 5", false},
		{"cte with", "WITH x AS (SELECT 1 AS n) SELECT * FROM x", false},
		{"ponto e virgula final", "SELECT * FROM osquery_info;", false},
		{"read_file bloqueado", "SELECT * FROM read_file WHERE path = (SELECT 1)", true},
		{"read_file entre aspas duplas", `SELECT * FROM "read_file"`, true},
		{"read_file em maiusculas", "SELECT * FROM READ_FILE", true},
		{"curl bloqueado", "SELECT * FROM curl WHERE url = (SELECT 1)", true},
		{"attach bloqueado", "SELECT * FROM windows_optional_features", false},
		{"attach como nome", "SELECT * FROM attach", true},
		{"multiplas instrucoes", "SELECT 1; SELECT 2", true},
		{"nao-select", "PRAGMA table_info(processes)", true},
		{"vazio", "   ", true},
		{"literal mencionando read_file nao bloqueia", "SELECT name FROM programs WHERE name = 'read_file'", false},
		{"literal com aspas escapadas", "SELECT 'it''s curl' AS x FROM osquery_info", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateReadOnlyOsqueryQuery(tt.sql)
			if tt.wantErr && err == nil {
				t.Fatalf("esperava erro para %q", tt.sql)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("nao esperava erro para %q: %v", tt.sql, err)
			}
		})
	}
}
