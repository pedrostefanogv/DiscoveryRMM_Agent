package thirdparty

import (
	"context"
	"strings"

	"discovery/app/core/winget"
)

// WingetProvider usa o winget ja instalado no endpoint.
type WingetProvider struct {
	client *winget.Client
}

// NewWingetProvider devolve o provider do winget.
func NewWingetProvider(client *winget.Client) *WingetProvider {
	return &WingetProvider{client: client}
}

func (p *WingetProvider) Source() string { return SourceWinget }

func (p *WingetProvider) Inventory(ctx context.Context) ([]AppUpdate, error) {
	if p == nil || p.client == nil {
		return nil, nil
	}
	raw, err := p.client.ListUpgradableRich(ctx)
	if err != nil {
		// winget ausente do PATH e um caso normal em muitas frotas: nao e erro
		// fatal do inventario (o provider apenas nao contribui).
		if isWingetUnavailable(err.Error()) {
			return nil, nil
		}
		return nil, err
	}

	// JSON quando a versao suporta; TABELA caso contrario (winget 1.30 em pt-BR
	// responde "argumento nao reconhecido ... '--output'"). Descoberto em campo
	// em 2026-10-29: sem o fallback o provider ficaria inutil em frota real.
	if updates := ParseWingetUpgradable(raw); len(updates) > 0 {
		return updates, nil
	}
	return ParseWingetUpgradeTable(raw), nil
}

func (p *WingetProvider) Upgrade(ctx context.Context, id string) (string, error) {
	if !ValidWingetID(id) {
		return "", ErrInvalidAppID
	}
	return p.client.Upgrade(ctx, id)
}

// ParseWingetUpgradable parseia a saida de "winget upgrade --output json".
// Quando o winget nao suporta JSON (versao antiga), devolve vazio: a tabela
// localizada nao e um contrato confiavel para inventario.
func ParseWingetUpgradable(raw string) []AppUpdate {
	entries, ok := winget.ParseListJSON(raw)
	if !ok {
		return nil
	}
	updates := make([]AppUpdate, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if !ValidWingetID(id) {
			continue
		}
		available := entry.AvailableVersionValue()
		if available == "" || strings.EqualFold(available, strings.TrimSpace(entry.Version)) {
			continue
		}
		updates = append(updates, AppUpdate{
			Source:           SourceWinget,
			ID:               id,
			Name:             strings.TrimSpace(entry.Name),
			InstalledVersion: strings.TrimSpace(entry.Version),
			AvailableVersion: available,
		})
	}
	return updates
}

// ValidWingetID aceita IDs do winget (letras, digitos, ponto, hifen, underscore
// e barra — alguns pacotes usam "Vendor/App").
func ValidWingetID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 200 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_', r == '/', r == '+':
		default:
			return false
		}
	}
	return true
}

func isWingetUnavailable(text string) bool {
	normalized := strings.ToLower(text)
	for _, marker := range []string{"executable file not found", "cannot find the file", "nao encontrado", "not found"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
