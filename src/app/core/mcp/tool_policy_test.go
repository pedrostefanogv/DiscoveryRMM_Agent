package mcp

import "testing"

func TestNormalizeMCPToolPolicyMode(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "all"},
		{"all", "all"},
		{"ALL", "all"},
		{"allowlist", "allowlist"},
		{"  AllowList ", "allowlist"},
		{"deny", "all"}, // desconhecido → compat
	}
	for _, tc := range cases {
		if got := NormalizeMCPToolPolicyMode(tc.in); got != tc.want {
			t.Fatalf("NormalizeMCPToolPolicyMode(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestToolAllowedByPolicy(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		allow   []string
		tool    string
		wantErr bool
	}{
		{"modo vazio (default all): permitido", "", nil, "install_package", false},
		{"modo all: permitido mesmo fora de allow", "all", []string{"ping"}, "install_package", false},
		{"modo desconhecido: permitido", "weird", nil, "ping", false},
		{"allowlist: tool na lista", "allowlist", []string{"Ping", "get_inventory"}, "ping", false},
		{"allowlist: trim/case-insensitive", "allowlist", []string{"  get_inventory "}, "GET_INVENTORY", false},
		{"allowlist: tool fora da lista recusa", "allowlist", []string{"ping"}, "install_package", true},
		{"allowlist vazia: tudo recusa", "allowlist", nil, "ping", true},
		{"allowlist: nome vazio recusa", "allowlist", []string{"ping"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ToolAllowedByPolicy(tc.mode, tc.allow, tc.tool)
			if tc.wantErr && err == nil {
				t.Fatalf("esperado erro tool_not_allowed para %q", tc.tool)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("inesperado erro: %v", err)
			}
		})
	}
}

func TestToolNotAllowedErrorMessage(t *testing.T) {
	err := ToolAllowedByPolicy("allowlist", []string{"ping"}, "uninstall_package")
	if err == nil {
		t.Fatal("esperado erro")
	}
	if got := err.Error(); got[:16] != "tool_not_allowed" {
		t.Fatalf("mensagem deve começar com tool_not_allowed, got %q", got)
	}
}
