package inventory

import (
	"context"
	"fmt"
	"strings"

	"discovery/app/core/thirdparty"
	"discovery/app/core/windowsupdate"
)

// WU-19/WU-20: apps de terceiros no inventario de patches.
//
// Os providers (Chocolatey/winget) ja existiam no agente para deploy de
// software; aqui eles alimentam o MESMO inventario de patches, com chave
// canonica "choco:<id>" / "winget:<id>". Aprovacao, politica, execucao e
// resultado reaproveitam o caminho que ja existe para WUA — nao ha um segundo
// pipeline de patches.

// ThirdPartyInstallOutcome e o resultado do upgrade de UM app.
type ThirdPartyInstallOutcome struct {
	UpdateKey string
	Error     string
}

// ThirdPartyInstallRequest descreve UM app a instalar. Quando traz URL+SHA-256
// do catálogo curado (WU-21), o agente baixa, verifica e executa o instalador;
// sem esses campos, delega ao gerenciador (winget/choco) como antes.
type ThirdPartyInstallRequest struct {
	ID            string
	InstallerURL  string
	SHA256        string
	SilentArgs    string
	InstallerType string
}

// collectThirdPartyUpdates roda os providers e converte o resultado para o
// formato do inventario. Falha de provider nao derruba o scan do WUA.
func (s *Service) collectThirdPartyUpdates(ctx context.Context) ([]windowsupdate.Update, map[string]error) {
	providers := s.thirdPartyProvidersSnapshot()
	if len(providers) == 0 {
		return nil, nil
	}
	apps, failures := thirdparty.Collect(ctx, providers)
	return thirdPartyUpdates(apps), failures
}

// thirdPartyUpdates converte apps com atualizacao disponivel em Update.
func thirdPartyUpdates(apps []thirdparty.AppUpdate) []windowsupdate.Update {
	updates := make([]windowsupdate.Update, 0, len(apps))
	for _, app := range apps {
		title := strings.TrimSpace(app.Name)
		if title == "" {
			title = app.ID
		}
		description := "Aplicativo de terceiros"
		if app.InstalledVersion != "" || app.AvailableVersion != "" {
			description = fmt.Sprintf("%s: %s -> %s", app.Source, app.InstalledVersion, app.AvailableVersion)
		}
		updates = append(updates, windowsupdate.Update{
			UpdateKey:      app.UpdateKey(),
			Title:          truncateForLog(title+" ("+app.ID+")", 500),
			Description:    truncateForLog(description, 2000),
			Classification: "Third-party",
			Categories:     []string{"Third Party"},
			IsPending:        true,
			Source:           app.Source,
			UpdateType:       "software",
			AvailableVersion: app.AvailableVersion,
		})
	}
	return updates
}

// InstallThirdParty instala/atualiza apps de terceiros (WU-19/WU-20/WU-21).
//
// Dois caminhos:
//  - com metadados de CATÁLOGO (URL + SHA-256): baixa do fornecedor, valida o
//    hash e executa os switches curados — não depende do gerenciador;
//  - sem metadados: delega ao provider (comportamento anterior).
//
// O ID é validado pela allowlist (provider ou instalador), nunca no shell.
func (s *Service) InstallThirdParty(ctx context.Context, source string, requests []ThirdPartyInstallRequest) ([]ThirdPartyInstallOutcome, error) {
	provider := thirdparty.FindProvider(s.thirdPartyProvidersSnapshot(), source)
	normalizedSource := strings.ToLower(strings.TrimSpace(source))
	if !thirdparty.IsThirdPartySource(normalizedSource) {
		return nil, fmt.Errorf("origem de terceiros invalida: %q", source)
	}

	hasCurated := false
	for _, request := range requests {
		if strings.TrimSpace(request.InstallerURL) != "" && strings.TrimSpace(request.SHA256) != "" {
			hasCurated = true
			break
		}
	}
	if provider == nil && !hasCurated {
		return nil, fmt.Errorf("gerenciador de terceiros %q indisponivel neste endpoint", source)
	}

	installer := thirdparty.NewInstaller()
	outcomes := make([]ThirdPartyInstallOutcome, 0, len(requests))
	for _, request := range requests {
		id := strings.TrimSpace(request.ID)
		if id == "" {
			continue
		}
		updateKey := normalizedSource + ":" + id
		spec := thirdparty.InstallSpec{
			Source:        normalizedSource,
			ID:            id,
			InstallerURL:  strings.TrimSpace(request.InstallerURL),
			SHA256:        strings.TrimSpace(request.SHA256),
			SilentArgs:    strings.TrimSpace(request.SilentArgs),
			InstallerType: strings.TrimSpace(request.InstallerType),
		}

		if spec.HasCatalogIntegrity() {
			s.logf("[windows-update] instalando " + updateKey + " pelo catalogo (hash verificado)")
			if _, err := installer.Install(ctx, spec); err != nil {
				outcomes = append(outcomes, ThirdPartyInstallOutcome{UpdateKey: updateKey, Error: err.Error()})
				continue
			}
			outcomes = append(outcomes, ThirdPartyInstallOutcome{UpdateKey: updateKey})
			continue
		}

		if provider == nil {
			outcomes = append(outcomes, ThirdPartyInstallOutcome{
				UpdateKey: updateKey,
				Error:     "app sem catalogo e gerenciador indisponivel",
			})
			continue
		}
		if _, err := provider.Upgrade(ctx, id); err != nil {
			outcomes = append(outcomes, ThirdPartyInstallOutcome{UpdateKey: updateKey, Error: err.Error()})
			continue
		}
		outcomes = append(outcomes, ThirdPartyInstallOutcome{UpdateKey: updateKey})
	}
	return outcomes, nil
}
