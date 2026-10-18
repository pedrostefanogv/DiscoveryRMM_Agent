package app

// Testes do NOME dos named pipes do IPC (regressão de 2026-10-17).
//
// Incidente: o nome do pipe admin estava como `\.pipediscovery-agent-ipc-admin`
// — faltava o prefixo `\\.\pipe\`. O serviço subia, o pipe de leitura
// funcionava e TODA mutação (chamados, comentários, anexos, config, updates)
// falhava com "canal admin indisponível", enquanto as leituras seguiam OK.
// Estes testes rodam em qualquer SO e travam o nome exato usado em produção.

import (
	"strings"
	"testing"
)

// TestIPCPipeAdminNameLiteral fixa o caminho EXATO do canal admin: qualquer
// alteração no literal (um typo de barra, por exemplo) quebra aqui.
func TestIPCPipeAdminNameLiteral(t *testing.T) {
	const want = `\\.\pipe\discovery-agent-ipc-admin`
	if IPCPipeAdminName != want {
		t.Fatalf("IPCPipeAdminName = %q, esperado %q", IPCPipeAdminName, want)
	}
	// Regressão explícita: o valor quebrado de 1.2.1 NÃO pode voltar.
	if IPCPipeAdminName == `\.pipediscovery-agent-ipc-admin` {
		t.Fatal("IPCPipeAdminName voltou ao valor malformado do incidente de 2026-10-17")
	}
	if err := validateIPCPipeName(IPCPipeAdminName); err != nil {
		t.Fatalf("validateIPCPipeName(%q): %v", IPCPipeAdminName, err)
	}
}

// TestValidateIPCPipeName cobre os nomes aceitos e recusados pelo validador
// de startup (o erro do SO é opaco: "Incorrect function").
func TestValidateIPCPipeName(t *testing.T) {
	valid := []string{
		`\\.\pipe\discovery-agent-ipc`,
		`\\.\pipe\discovery-agent-ipc-admin`,
		`\\.\pipe\a`,
		`\\.\pipe\A.b_c-d`,
		ipcPipePrefix + strings.Repeat("a", ipcPipeLeafMaxLen),
	}
	for _, name := range valid {
		if err := validateIPCPipeName(name); err != nil {
			t.Errorf("validateIPCPipeName(%q) = %v, esperado nil", name, err)
		}
	}

	invalid := []string{
		"",
		"discovery-agent-ipc",
		`\.pipediscovery-agent-ipc-admin`, // valor do incidente
		`\.\pipe\discovery-agent-ipc`,     // uma barra a menos
		`\\.pipe\discovery-agent-ipc`,     // prefixo truncado
		ipcPipePrefix,                     // sem nome
		`\\.\pipe\com espaço`,
		`\\.\pipe\a\b`,
		`\\.\pipe\a*b`,
		ipcPipePrefix + strings.Repeat("a", ipcPipeLeafMaxLen+1),
	}
	for _, name := range invalid {
		if err := validateIPCPipeName(name); err == nil {
			t.Errorf("validateIPCPipeName(%q) = nil, esperado erro", name)
		}
	}
}
