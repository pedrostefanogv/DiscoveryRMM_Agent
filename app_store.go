package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"discovery/internal/models"
)

const (
	appStoreCacheKey       = "app_store_effective"
	appStoreMemoryCacheTTL = 2 * time.Minute
	appStoreSQLiteCacheTTL = 30 * 24 * time.Hour
)

func normalizeAppStoreInstallationType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "winget":
		return string(AppStoreInstallationWinget)
	case "chocolatey":
		return string(AppStoreInstallationChocolatey)
	default:
		return strings.TrimSpace(value)
	}
}

func appStoreLookupKey(installationType, packageID string) string {
	return strings.ToLower(strings.TrimSpace(installationType)) + "|" + strings.ToLower(strings.TrimSpace(packageID))
}

func (a *App) fetchAppStoreByInstallationType(ctx context.Context, installationType AppStoreInstallationType) (AppStoreResponse, error) {
	cfg := a.GetDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if apiServer == "" || token == "" {
		return AppStoreResponse{}, fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
	}
	if apiScheme != "http" && apiScheme != "https" {
		return AppStoreResponse{}, fmt.Errorf("apiScheme inválido: use http ou https")
	}

	target := apiScheme + "://" + apiServer + "/api/agent-auth/me/app-store"
	parsed, err := url.Parse(target)
	if err != nil {
		return AppStoreResponse{}, fmt.Errorf("URL inválida: %w", err)
	}
	query := parsed.Query()
	query.Set("installationType", string(installationType))
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return AppStoreResponse{}, fmt.Errorf("falha ao criar request da app-store: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	setAgentAuthHeaders(req, token)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return AppStoreResponse{}, fmt.Errorf("falha ao chamar app-store (%s): %w", installationType, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AppStoreResponse{}, fmt.Errorf("app-store (%s) retornou HTTP %s: %s", installationType, resp.Status, strings.TrimSpace(string(body)))
	}

	var payload AppStoreResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return AppStoreResponse{}, fmt.Errorf("resposta inválida da app-store (%s): %w", installationType, err)
	}

	payload.InstallationType = normalizeAppStoreInstallationType(payload.InstallationType)
	if payload.InstallationType == "" {
		payload.InstallationType = string(installationType)
	}
	if payload.Items == nil {
		payload.Items = []AppStoreItem{}
	}
	for i := range payload.Items {
		payload.Items[i].InstallationType = normalizeAppStoreInstallationType(payload.Items[i].InstallationType)
		if payload.Items[i].InstallationType == "" {
			payload.Items[i].InstallationType = payload.InstallationType
		}
		payload.Items[i].PackageID = strings.TrimSpace(payload.Items[i].PackageID)
	}

	return payload, nil
}

func (a *App) loadEffectiveAppStorePolicy(ctx context.Context, forceRefresh bool) (AppStoreEffectivePolicy, error) {
	if !forceRefresh {
		if cached, ok := a.appStorePolicy.get(appStoreMemoryCacheTTL); ok {
			return cached, nil
		}
		if a.db != nil {
			var persisted AppStoreEffectivePolicy
			found, err := a.db.CacheGetJSON(appStoreCacheKey, &persisted)
			if err == nil && found {
				a.appStorePolicy.set(persisted)
				return persisted, nil
			}
		}
	}

	results := make([]AppStoreResponse, 0, 2)
	for _, installationType := range []AppStoreInstallationType{AppStoreInstallationWinget, AppStoreInstallationChocolatey} {
		payload, err := a.fetchAppStoreByInstallationType(ctx, installationType)
		if err != nil {
			a.supportLogf("falha ao carregar app-store (%s): %v", installationType, err)
			return AppStoreEffectivePolicy{}, err
		}
		results = append(results, payload)
	}

	lookup := make(map[string]AppStoreItem)
	for _, payload := range results {
		for _, item := range payload.Items {
			if strings.TrimSpace(item.PackageID) == "" {
				continue
			}
			key := appStoreLookupKey(item.InstallationType, item.PackageID)
			lookup[key] = item
		}
	}

	items := make([]AppStoreItem, 0, len(lookup))
	for _, item := range lookup {
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		leftType := strings.ToLower(items[i].InstallationType)
		rightType := strings.ToLower(items[j].InstallationType)
		if leftType != rightType {
			return leftType < rightType
		}
		return strings.ToLower(items[i].PackageID) < strings.ToLower(items[j].PackageID)
	})

	policy := AppStoreEffectivePolicy{
		Items:     items,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	a.appStorePolicy.set(policy)
	if a.db != nil {
		if err := a.db.CacheSetJSON(appStoreCacheKey, policy, appStoreSQLiteCacheTTL); err != nil {
			a.supportLogf("aviso: falha ao salvar cache da app-store: %v", err)
		}
	}

	a.supportLogf("app-store efetiva carregada: %d item(ns)", len(policy.Items))
	return policy, nil
}

func (a *App) getCatalogFromAppStore(ctx context.Context) (models.Catalog, error) {
	policy, err := a.loadEffectiveAppStorePolicy(ctx, false)
	if err != nil {
		return models.Catalog{}, err
	}

	packages := make([]models.AppItem, 0, len(policy.Items))
	withIcon := 0
	for _, item := range policy.Items {
		category := strings.TrimSpace(item.SourceScope)
		if category == "" {
			category = item.InstallationType
		}
		appItem := models.AppItem{
			ID:             strings.TrimSpace(item.PackageID),
			Name:           strings.TrimSpace(item.Name),
			Publisher:      strings.TrimSpace(item.Publisher),
			Version:        strings.TrimSpace(item.Version),
			Description:    strings.TrimSpace(item.Description),
			InstallCommand: strings.TrimSpace(item.InstallCommand),
			Category:       category,
			Icon:           strings.TrimSpace(item.IconURL),
		}
		if appItem.Name == "" {
			appItem.Name = appItem.ID
		}
		if appItem.Icon != "" {
			withIcon++
		}
		packages = append(packages, appItem)
	}

	return models.Catalog{
		Generated:        time.Now().UTC().Format(time.RFC3339),
		Count:            len(packages),
		PackagesWithIcon: withIcon,
		Packages:         packages,
	}, nil
}

func (a *App) findAllowedPackage(ctx context.Context, installationType, packageID string) (AppStoreItem, error) {
	instType := normalizeAppStoreInstallationType(installationType)
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return AppStoreItem{}, fmt.Errorf("packageId obrigatório")
	}

	policy, err := a.loadEffectiveAppStorePolicy(ctx, false)
	if err != nil {
		return AppStoreItem{}, fmt.Errorf("não foi possível validar política de app-store: %w", err)
	}

	for _, item := range policy.Items {
		if strings.EqualFold(item.InstallationType, instType) && strings.EqualFold(item.PackageID, packageID) {
			return item, nil
		}
	}

	return AppStoreItem{}, fmt.Errorf("pacote %q (%s) não autorizado para este agent", packageID, instType)
}

func (a *App) resolveAllowedPackage(ctx context.Context, packageID string) (AppStoreItem, error) {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return AppStoreItem{}, fmt.Errorf("packageId obrigatório")
	}

	policy, err := a.loadEffectiveAppStorePolicy(ctx, false)
	if err != nil {
		return AppStoreItem{}, fmt.Errorf("não foi possível validar política de app-store: %w", err)
	}

	matches := make([]AppStoreItem, 0, 2)
	for _, item := range policy.Items {
		if strings.EqualFold(item.PackageID, packageID) {
			matches = append(matches, item)
		}
	}

	if len(matches) == 0 {
		return AppStoreItem{}, fmt.Errorf("pacote %q não autorizado para este agent", packageID)
	}
	if len(matches) > 1 {
		return AppStoreItem{}, fmt.Errorf("pacote %q está ambíguo em múltiplos installationType; use identificação mais específica", packageID)
	}

	return matches[0], nil
}

func (a *App) authorizeAutomationPackage(ctx context.Context, installationType, packageID, operation string) error {
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation == "uninstall" {
		// Uninstall está fora do escopo de bloqueio desta rodada.
		return nil
	}
	_, err := a.findAllowedPackage(ctx, installationType, packageID)
	return err
}
