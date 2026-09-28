package winget

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
)

// jsonOutputUnsupported memoriza que a versão do winget não reconhece
// "--output json". Sem isso, cada listagem gastaria um processo winget extra
// falhando antes de cair para a tabela.
var jsonOutputUnsupported atomic.Bool

// PackageEntry é um item da saída JSON do winget ("list/upgrade --output json").
// Os Ids vêm completos — sem o truncamento da coluna Id da tabela, que fazia a
// decisão de instalação falhar para identificadores longos.
type PackageEntry struct {
	Name             string `json:"Name"`
	ID               string `json:"Id"`
	Version          string `json:"Version"`
	AvailableVersion string `json:"AvailableVersion"`
	Available        string `json:"Available"`
	Source           string `json:"Source"`
}

// AvailableVersionValue devolve a versão disponível cobrindo as duas chaves
// usadas pelas versões do winget (AvailableVersion / Available).
func (e PackageEntry) AvailableVersionValue() string {
	if value := strings.TrimSpace(e.AvailableVersion); value != "" {
		return value
	}
	return strings.TrimSpace(e.Available)
}

// ParseListJSON parseia a saída "‑‑output json" do winget. Retorna ok=false
// quando o texto não é um array JSON (ex.: winget antigo devolveu help/erro).
func ParseListJSON(raw string) ([]PackageEntry, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "[") {
		var entries []PackageEntry
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, false
		}
		return entries, true
	}
	// Algumas variantes embrulham a lista em um objeto ({"Packages":[...]}).
	if strings.HasPrefix(trimmed, "{") {
		var wrapper struct {
			Packages []PackageEntry `json:"Packages"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil {
			return nil, false
		}
		if wrapper.Packages == nil {
			return nil, false
		}
		return wrapper.Packages, true
	}
	return nil, false
}

// LooksLikeJSON indica se o texto é um array JSON válido.
func LooksLikeJSON(raw string) bool {
	_, ok := ParseListJSON(raw)
	return ok
}

// ListInstalledRich tenta "list --output json" (Ids completos) e cai para a
// tabela quando a versão do winget não reconhece o argumento. O texto devolvido
// pode ser JSON ou tabela — os parsers detectam o formato.
func (c *Client) ListInstalledRich(ctx context.Context) (string, error) {
	if !jsonOutputUnsupported.Load() {
		out, err := c.run(ctx, "list", "--output", "json", "--accept-source-agreements", "--disable-interactivity")
		// O JSON é autoritativo mesmo que o winget tenha saído com erro depois de
		// imprimi-lo (ex.: uma fonte com problema no fim da listagem).
		if LooksLikeJSON(out) {
			return out, nil
		}
		if isUnknownJSONArgumentError(err, out) {
			jsonOutputUnsupported.Store(true)
		}
	}
	return c.ListInstalled(ctx)
}

// ListUpgradableRich é o equivalente para "winget upgrade".
func (c *Client) ListUpgradableRich(ctx context.Context) (string, error) {
	if !jsonOutputUnsupported.Load() {
		out, err := c.run(ctx, "upgrade", "--output", "json", "--accept-source-agreements", "--disable-interactivity")
		if LooksLikeJSON(out) {
			return out, nil
		}
		if isUnknownJSONArgumentError(err, out) {
			jsonOutputUnsupported.Store(true)
		}
	}
	return c.ListUpgradable(ctx)
}

// isUnknownJSONArgumentError detecta a rejeição do argumento "--output" (winget
// sem suporte a JSON), que deve desligar as tentativas seguintes.
func isUnknownJSONArgumentError(err error, output string) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(output + " " + err.Error())
	for _, marker := range []string{
		"not recognized",
		"não reconhecido",
		"nao reconhecido",
		"unrecognized",
		"unknown argument",
		// winget ausente do PATH: não há motivo para tentar o JSON novamente.
		"executable file not found",
		"cannot find the file",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
