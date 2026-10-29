package thirdparty

import (
	"context"
	"regexp"
	"strings"

	"discovery/app/core/chocolatey"
)

var chocoIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ChocoProvider usa o Chocolatey ja instalado no endpoint.
type ChocoProvider struct {
	client *chocolatey.Client
}

// NewChocoProvider devolve o provider do Chocolatey.
func NewChocoProvider(client *chocolatey.Client) *ChocoProvider {
	return &ChocoProvider{client: client}
}

func (p *ChocoProvider) Source() string { return SourceChoco }

func (p *ChocoProvider) Inventory(ctx context.Context) ([]AppUpdate, error) {
	if p == nil || p.client == nil {
		return nil, nil
	}
	raw, err := p.client.ListUpgradable(ctx)
	if err != nil {
		return nil, err
	}
	return ParseChocoOutdated(raw), nil
}

func (p *ChocoProvider) Upgrade(ctx context.Context, id string) (string, error) {
	if !ValidAppID(id) {
		return "", ErrInvalidAppID
	}
	return p.client.Upgrade(ctx, id)
}

// ParseChocoOutdated parseia a saida de "choco outdated --limit-output":
//
//	id|versao_atual|versao_disponivel|pinned
//
// Linhas vazias, cabecalhos e linhas com menos de 3 campos sao ignoradas.
func ParseChocoOutdated(raw string) []AppUpdate {
	updates := make([]AppUpdate, 0, 8)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "|") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		if !ValidAppID(id) {
			continue
		}
		// "pinned" (4o campo) = atualizacao bloqueada pelo operador: nao entra.
		if len(parts) >= 4 && strings.EqualFold(strings.TrimSpace(parts[3]), "true") {
			continue
		}
		updates = append(updates, AppUpdate{
			Source:           SourceChoco,
			ID:               id,
			Name:             id,
			InstalledVersion: strings.TrimSpace(parts[1]),
			AvailableVersion: strings.TrimSpace(parts[2]),
		})
	}
	return updates
}

// ValidAppID valida o identificador antes de virar argumento de processo.
func ValidAppID(id string) bool {
	id = strings.TrimSpace(id)
	return id != "" && len(id) <= 128 && chocoIDPattern.MatchString(id)
}
