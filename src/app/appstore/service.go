package appstore

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

	"discovery/app/core/database"
	"discovery/app/core/models"
	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

const (
	CacheKey       = "app_store_effective"
	MemoryCacheTTL = 2 * time.Minute
	SQLiteCacheTTL = 30 * 24 * time.Hour
)

// DebugConfig é uma visão mínima da configuração de debug usada pela app-store.
type DebugConfig struct {
	ApiScheme string
	ApiServer string
	AuthToken string
	AgentID   string
}

// Deps são as dependências injetadas no Service.
type Deps struct {
	// GetDebugConfig retorna a configuração de debug.
	GetDebugConfig func() DebugConfig
	// GetAgentConfiguration retorna a configuração do agente.
	GetAgentConfiguration func() AgentConfiguration
	// FeatureEnabled verifica se uma flag de feature está habilitada.
	FeatureEnabled func(*bool) bool
	// Logf appends a log line.
	Logf func(string)
	// DB retorna o banco de dados (pode ser nil).
	DB func() *database.DB
	// Cache retorna o cache de política.
	Cache *PolicyCache
}

// AgentConfiguration é uma visão mínima da configuração do agente.
type AgentConfiguration struct {
	AppStoreEnabled *bool
}

// Service encapsula a lógica de app-store (fetch, cache e política efetiva).
type Service struct {
	getDebugConfig        func() DebugConfig
	getAgentConfiguration func() AgentConfiguration
	featureEnabled        func(*bool) bool
	logf                  func(string)
	db                    func() *database.DB
	cache                 *PolicyCache
}

// New cria um AppStoreService.
func New(deps Deps) *Service {
	logf := deps.Logf
	if logf == nil {
		logf = func(string) {}
	}
	return &Service{
		getDebugConfig:        deps.GetDebugConfig,
		getAgentConfiguration: deps.GetAgentConfiguration,
		featureEnabled:        deps.FeatureEnabled,
		logf:                  logf,
		db:                    deps.DB,
		cache:                 deps.Cache,
	}
}

// NormalizeInstallationType normaliza o tipo de instalação.
func NormalizeInstallationType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "winget":
		return string(InstallationWinget)
	case "chocolatey":
		return string(InstallationChocolatey)
	default:
		return strings.TrimSpace(value)
	}
}

func lookupKey(installationType, packageID string) string {
	return strings.ToLower(strings.TrimSpace(installationType)) + "|" + strings.ToLower(strings.TrimSpace(packageID))
}

// FetchByInstallationType coleta todas as páginas via cursor pagination (CQRS).
func (s *Service) FetchByInstallationType(ctx context.Context, installationType InstallationType) (Response, error) {
	cfg := s.getDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)
	token := strings.TrimSpace(cfg.AuthToken)
	if apiServer == "" || token == "" {
		return Response{}, fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
	}
	if apiScheme != "http" && apiScheme != "https" {
		return Response{}, fmt.Errorf("apiScheme inválido: use http ou https")
	}

	var allItems []Item
	cursor := ""
	pageCount := 0
	maxPages := 20 // safety limit

	for {
		pageCount++
		if pageCount > maxPages {
			return Response{}, fmt.Errorf("app-store (%s) excedeu limite de %d paginas", installationType, maxPages)
		}

		page, err := s.FetchPage(ctx, installationType, cursor, token, cfg.AgentID)
		if err != nil {
			return Response{}, err
		}

		allItems = append(allItems, page.Items...)

		if !page.HasMore || strings.TrimSpace(page.NextCursor) == "" {
			return Response{
				InstallationType: string(installationType),
				Count:            len(allItems),
				Items:            allItems,
			}, nil
		}

		cursor = strings.TrimSpace(page.NextCursor)
		s.logf(fmt.Sprintf("app-store (%s) pagina %d carregada, proximo cursor: %s", installationType, pageCount, cursor))
	}
}

// FetchPage faz uma única requisição ao endpoint app-store com cursor opcional.
func (s *Service) FetchPage(ctx context.Context, installationType InstallationType, cursor, token, agentID string) (Response, error) {
	cfg := s.getDebugConfig()
	apiScheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	apiServer := strings.TrimSpace(cfg.ApiServer)

	target := apiScheme + "://" + apiServer + "/api/v1/agent-auth/me/app-store"
	parsed, err := url.Parse(target)
	if err != nil {
		return Response{}, fmt.Errorf("URL inválida: %w", err)
	}
	query := parsed.Query()
	query.Set("installationType", string(installationType))
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	// Informa à API o pageSize esperado pelo agent
	query.Set("pageSize", "200")
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Response{}, fmt.Errorf("falha ao criar request da app-store: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, token, agentID); err != nil {
		return Response{}, err
	}

	resp, err := tlsutil.NewHTTPClient(15 * time.Second).Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("falha ao chamar app-store (%s): %w", installationType, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("app-store (%s) retornou HTTP %s: %s", installationType, resp.Status, strings.TrimSpace(string(body)))
	}

	var payload Response
	if err := json.Unmarshal(body, &payload); err != nil {
		return Response{}, fmt.Errorf("resposta inválida da app-store (%s): %w", installationType, err)
	}

	payload.InstallationType = NormalizeInstallationType(payload.InstallationType)
	if payload.InstallationType == "" {
		payload.InstallationType = string(installationType)
	}
	if payload.Items == nil {
		payload.Items = []Item{}
	}
	for i := range payload.Items {
		payload.Items[i].InstallationType = NormalizeInstallationType(payload.Items[i].InstallationType)
		if payload.Items[i].InstallationType == "" {
			payload.Items[i].InstallationType = payload.InstallationType
		}
		payload.Items[i].PackageID = strings.TrimSpace(payload.Items[i].PackageID)
	}

	return payload, nil
}

// LoadEffectivePolicy carrega a política efetiva da app-store.
func (s *Service) LoadEffectivePolicy(ctx context.Context, forceRefresh bool) (EffectivePolicy, error) {
	if !s.featureEnabled(s.getAgentConfiguration().AppStoreEnabled) {
		return EffectivePolicy{}, fmt.Errorf("app store desabilitada pela configuração do agente")
	}

	if !forceRefresh {
		if cached, ok := s.cache.Get(MemoryCacheTTL); ok {
			return cached, nil
		}
		if s.db != nil {
			var persisted EffectivePolicy
			found, err := s.db().CacheGetJSON(CacheKey, &persisted)
			if err == nil && found {
				s.cache.Set(persisted)
				return persisted, nil
			}
		}
	}

	results := make([]Response, 0, 2)
	for _, installationType := range []InstallationType{InstallationWinget, InstallationChocolatey} {
		payload, err := s.FetchByInstallationType(ctx, installationType)
		if err != nil {
			s.logf(fmt.Sprintf("falha ao carregar app-store (%s): %v", installationType, err))
			return EffectivePolicy{}, err
		}
		results = append(results, payload)
	}

	lookup := make(map[string]Item)
	for _, payload := range results {
		for _, item := range payload.Items {
			if strings.TrimSpace(item.PackageID) == "" {
				continue
			}
			key := lookupKey(item.InstallationType, item.PackageID)
			lookup[key] = item
		}
	}

	items := make([]Item, 0, len(lookup))
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

	policy := EffectivePolicy{
		Items:     items,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.cache.Set(policy)
	if s.db != nil {
		if err := s.db().CacheSetJSON(CacheKey, policy, SQLiteCacheTTL); err != nil {
			s.logf(fmt.Sprintf("aviso: falha ao salvar cache da app-store: %v", err))
		}
	}

	s.logf(fmt.Sprintf("app-store efetiva carregada: %d item(ns)", len(policy.Items)))
	return policy, nil
}

// GetCatalogFromAppStore converte a política efetiva em um catálogo.
func (s *Service) GetCatalogFromAppStore(ctx context.Context) (models.Catalog, error) {
	policy, err := s.LoadEffectivePolicy(ctx, false)
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
			SilentCommand:  strings.TrimSpace(item.SilentCommand),
			// Fallback: se Silent não veio do feed, usa SilentWithProgress.
			SilentWithProgress: strings.TrimSpace(item.SilentWithProgress),
			Category:           category,
			Icon:               strings.TrimSpace(item.IconURL),
		}
		if appItem.SilentCommand == "" {
			appItem.SilentCommand = appItem.SilentWithProgress
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

// FindAllowedPackage valida se um pacote está autorizado para o agent.
func (s *Service) FindAllowedPackage(ctx context.Context, installationType, packageID string) (Item, error) {
	instType := NormalizeInstallationType(installationType)
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return Item{}, fmt.Errorf("packageId obrigatório")
	}

	policy, err := s.LoadEffectivePolicy(ctx, false)
	if err != nil {
		return Item{}, fmt.Errorf("não foi possível validar política de app-store: %w", err)
	}

	for _, item := range policy.Items {
		if strings.EqualFold(item.InstallationType, instType) && strings.EqualFold(item.PackageID, packageID) {
			return item, nil
		}
	}

	return Item{}, fmt.Errorf("pacote %q (%s) não autorizado para este agent", packageID, instType)
}

// ResolveAllowedPackage resolve um pacote autorizado, detectando ambiguidade.
func (s *Service) ResolveAllowedPackage(ctx context.Context, packageID string) (Item, error) {
	packageID = strings.TrimSpace(packageID)
	if packageID == "" {
		return Item{}, fmt.Errorf("packageId obrigatório")
	}

	policy, err := s.LoadEffectivePolicy(ctx, false)
	if err != nil {
		return Item{}, fmt.Errorf("não foi possível validar política de app-store: %w", err)
	}

	matches := make([]Item, 0, 2)
	for _, item := range policy.Items {
		if strings.EqualFold(item.PackageID, packageID) {
			matches = append(matches, item)
		}
	}

	if len(matches) == 0 {
		return Item{}, fmt.Errorf("pacote %q não autorizado para este agent", packageID)
	}
	if len(matches) > 1 {
		return Item{}, fmt.Errorf("pacote %q está ambíguo em múltiplos installationType; use identificação mais específica", packageID)
	}

	return matches[0], nil
}

// AuthorizeAutomationPackage autoriza um pacote para automação.
func (s *Service) AuthorizeAutomationPackage(ctx context.Context, installationType, packageID, operation string) error {
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation == "uninstall" {
		// Uninstall está fora do escopo de bloqueio desta rodada.
		return nil
	}
	_, err := s.FindAllowedPackage(ctx, installationType, packageID)
	return err
}
