package buildinfo

import "strings"

// Version is set at build time via:
// -ldflags "-X discovery/app/core/buildinfo.Version=x.y.z"
var Version = "0.0.0"

// Commit is set at build time via:
// -ldflags "-X discovery/app/core/buildinfo.Commit=<git rev-parse --short HEAD>"
var Commit = "unknown"

// isPlaceholderReport reporta se o valor é um default de build SEM injeção de
// ldflags ("0.0.0"/"unknown") ou vazio — não representa versão/commit real.
func isPlaceholderReport(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "" || v == "unknown" || v == "dev" || v == "0.0.0"
}

// CommitForReport retorna o commit curto para relato ao servidor (inventário /
// hardware report). Usa Commit quando injetado por ldflags; em builds locais
// sem ldflags cai para o VCS stamping do toolchain (Revision(), embutido pelo
// `go build` a partir de um checkout git). Retorna "" quando nenhum dos dois
// está disponível — nesse caso o servidor preserva o último valor conhecido
// em vez de sobrescrever com "unknown".
func CommitForReport() string {
	if c := strings.TrimSpace(Commit); !isPlaceholderReport(c) {
		return c
	}
	if r := Revision(); r != "" && r != "unknown" {
		return r
	}
	return ""
}
