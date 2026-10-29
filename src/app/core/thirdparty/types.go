// Package thirdparty deteta e instala atualizacoes de aplicativos de terceiros
// (Chocolatey e winget) — WU-19/WU-20 (decisao D21).
//
// Escopo desta primeira entrega: INVENTARIO e UPGRADE por app, reaproveitando
// os clients que o agente ja usa para deploy de software (nao ha um segundo
// caminho de execucao). O catalogo MSI curado sobre winget-pkgs (WU-21) fica
// para a proxima etapa.
//
// Regras de seguranca:
//  - o ID do pacote e validado por allowlist de caracteres ANTES de virar
//    argumento de processo (nunca shell);
//  - falha de um provider nao derruba o inventario (agrega o que deu certo);
//  - "sem atualizacao" nao e erro.
package thirdparty

import (
	"context"
	"strings"
)

// Origens suportadas (valor estavel no envelope/API).
const (
	SourceChoco  = "choco"
	SourceWinget = "winget"
)

// AppUpdate e uma atualizacao de aplicativo de terceiros disponivel.
type AppUpdate struct {
	Source           string `json:"source"`
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	AvailableVersion string `json:"availableVersion,omitempty"`
}

// UpdateKey devolve a chave canonica usada no inventario/aprovacao.
func (u AppUpdate) UpdateKey() string {
	return u.Source + ":" + u.ID
}

// Provider abstrai um gerenciador de pacotes de terceiros.
type Provider interface {
	// Source devolve choco|winget.
	Source() string
	// Inventory lista apps com atualizacao disponivel.
	Inventory(ctx context.Context) ([]AppUpdate, error)
	// Upgrade atualiza UM app pelo id validado.
	Upgrade(ctx context.Context, id string) (string, error)
}

// Collect agrega o inventario de todos os providers, ignorando os que falham.
// Devolve os updates encontrados e os providers que falharam (para log).
func Collect(ctx context.Context, providers []Provider) ([]AppUpdate, map[string]error) {
	updates := make([]AppUpdate, 0, 16)
	failures := make(map[string]error, len(providers))

	for _, provider := range providers {
		if provider == nil {
			continue
		}
		found, err := provider.Inventory(ctx)
		if err != nil {
			failures[provider.Source()] = err
			continue
		}
		updates = append(updates, found...)
	}
	return updates, failures
}

// FindProvider devolve o provider da origem informada.
func FindProvider(providers []Provider, source string) Provider {
	source = strings.ToLower(strings.TrimSpace(source))
	for _, provider := range providers {
		if provider != nil && strings.EqualFold(provider.Source(), source) {
			return provider
		}
	}
	return nil
}

// IsThirdPartySource informa se a origem e de aplicativo de terceiros.
func IsThirdPartySource(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case SourceChoco, SourceWinget:
		return true
	default:
		return false
	}
}
