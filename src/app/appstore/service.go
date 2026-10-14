package appstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"discovery/app/core/database"
	"discovery/app/core/memlimit"
	"discovery/app/core/models"
	"discovery/app/core/tlsutil"
	"discovery/app/netutil"
)

const (
	CacheKey = "app_store_effective"
	// CacheKeyCompressed é a chave do cache persistido comprimido (gzip).
	//
	// Motivação (otimização de RAM): a política efetiva em JSON puro ocupava
	// ~31,6 MB num ÚNICO value TEXT — com 27.374 itens — e era lida inteira para
	// a memória (string + cópia []byte + objeto decodificado ≈ 95 MB de pico),
	// enchendo o page cache do SQLite. Com gzip o mesmo payload cai para poucos
	// MB no arquivo e o pico de leitura cai na mesma proporção.
	//
	// A chave antiga (CacheKey) continua sendo lida como fallback de
	// compatibilidade e é removida após a migração bem-sucedida para v2.
	CacheKeyCompressed = "app_store_effective_v2"
	// MemoryCacheTTL define por quanto tempo a política fica válida em memória.
	// 30 minutos evita sobrecarregar o servidor com fetches frequentes; o cache
	// persistido em SQLite cobre o período entre expirações e reinícios.
	MemoryCacheTTL = 30 * time.Minute
	// SQLiteCacheTTL define por quanto tempo a política persistida no SQLite
	// permanece válida (7 dias). Após expirar, um novo fetch é feito.
	SQLiteCacheTTL = 7 * 24 * time.Hour

	// appStorePageSize é o pageSize pedido ao endpoint me/app-store. O servidor
	// pagina de fato quando recebe paged=true (teto de 5000 do lado da API).
	//
	// Antes o servidor ignorava pageSize e devolvia os 27 mil itens numa única
	// resposta de ~30 MB; o agent bufferizava o corpo inteiro (io.ReadAll) para
	// depois decodificar — ~60 MB de pico só nessa etapa. Em páginas de 2000 o
	// pico do corpo cai para poucos MB por requisição.
	appStorePageSize = 2000
	// maxAppStorePages é o teto de segurança do loop de paginação
	// (60 × 2000 = 120 mil itens). Com os 27 mil do catálogo winget atual são
	// ~14 requisições.
	maxAppStorePages = 60
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

	// fetchMu serializa o fetch remoto: quando o cache expira, múltiplas
	// goroutines (catálogo, validação de pacote, etc.) podem disparar
	// LoadEffectivePolicy ao mesmo tempo. Sem serialização, cada uma faria
	// seu próprio download (stampede), sobrecarregando o servidor API.
	fetchMu sync.Mutex
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
	case "chocolatey", "choco":
		return string(InstallationChocolatey)
	case "custom":
		return string(InstallationCustom)
	default:
		return strings.TrimSpace(value)
	}
}

func lookupKey(installationType, packageID string) string {
	return strings.ToLower(strings.TrimSpace(installationType)) + "|" + strings.ToLower(strings.TrimSpace(packageID))
}

// installationTypeSortRank define a prioridade de exibição dos apps na loja:
// Winget primeiro (melhor integração), depois Chocolatey e Custom; tipos
// desconhecidos vão para o fim, mantendo ordem estável caso o servidor passe
// a enviar outros valores.
func installationTypeSortRank(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "winget":
		return 0
	case "chocolatey", "choco":
		return 1
	case "custom":
		return 2
	default:
		return 3
	}
}

// sortEffectiveItems ordena os itens efetivos da loja por prioridade de origem
// (Winget > Chocolatey > Custom > desconhecido) e, dentro da mesma origem,
// por packageId alfabético (case-insensitive). SliceStable mantém a ordem
// relativa de itens idênticos.
func sortEffectiveItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		leftRank := installationTypeSortRank(items[i].InstallationType)
		rightRank := installationTypeSortRank(items[j].InstallationType)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return strings.ToLower(items[i].PackageID) < strings.ToLower(items[j].PackageID)
	})
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
	maxPages := maxAppStorePages // safety limit

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
	// Pede paginação explícita: o servidor só fatia a resposta quando recebe
	// paged=true (agents antigos, que não enviam o flag, continuam recebendo a
	// coleção completa numa página — sem quebrar o limite de 20 páginas deles).
	query.Set("paged", "true")
	query.Set("pageSize", strconv.Itoa(appStorePageSize))
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

	// Política persistida expirada (lida uma única vez nesta chamada): guarda
	// como candidato a fallback stale caso o fetch remoto falhe. A leitura usa
	// CacheGetStale (não deleta a entrada) — ler com CacheGetJSON apagaria a
	// entrada expirada e tornaria o fallback stale impossível.
	var expiredPersisted *EffectivePolicy
	if !forceRefresh {
		if cached, ok := s.cache.Get(MemoryCacheTTL); ok {
			return cached, nil
		}
		if persisted, found := s.readPersistedPolicy(); found {
			if s.persistedPolicyFresh(persisted) {
				s.cache.Set(persisted)
				return persisted, nil
			}
			expired := persisted
			expiredPersisted = &expired
		}
	}

	// Singleflight: apenas uma goroutine faz o fetch remoto por vez. As demais
	// esperam e reavaliam o cache — se a primeira já populou, retornam dela
	// sem tocar no servidor.
	s.fetchMu.Lock()
	defer s.fetchMu.Unlock()

	if !forceRefresh {
		if cached, ok := s.cache.Get(MemoryCacheTTL); ok {
			return cached, nil
		}
	}

	policy, fetchErr := s.fetchEffectivePolicy(ctx)
	if fetchErr != nil {
		// Stale-on-error: se o fetch remoto falhar (API fora do ar, config
		// incompleta, timeout), serve a última política conhecida persistida
		// no SQLite MESMO expirada — a loja continua utilizável offline em vez
		// de quebrar. A entrada stale não é apagada nem estendida; o próximo
		// fetch bem-sucedido a substitui normalmente.
		if expiredPersisted != nil {
			s.logf(fmt.Sprintf("aviso: fetch da app-store falhou (%v); servindo cache stale do SQLite: %d item(ns), fetchedAt=%s",
				fetchErr, len(expiredPersisted.Items), expiredPersisted.FetchedAt))
			s.cache.Set(*expiredPersisted)
			return *expiredPersisted, nil
		}
		if stale, found := s.readPersistedPolicy(); found {
			s.logf(fmt.Sprintf("aviso: fetch da app-store falhou (%v); servindo cache stale do SQLite: %d item(ns), fetchedAt=%s",
				fetchErr, len(stale.Items), stale.FetchedAt))
			s.cache.Set(stale)
			return stale, nil
		}
		return EffectivePolicy{}, fetchErr
	}
	return policy, nil
}

// readPersistedPolicy lê a política persistida no SQLite sem verificar TTL e
// sem deletar a entrada (CacheGetStale/CacheGetBytesStale). found=false quando
// não há DB, a entrada não existe ou o JSON é inválido.
//
// Ordem: v2 comprimido (gzip) → v1 legado (JSON texto). O v1 é migrado para v2
// na primeira leitura bem-sucedida: é o que libera os ~31 MB de páginas do
// arquivo nas máquinas que já têm o cache antigo (funciona offline, sem
// depender de um fetch novo).
func (s *Service) readPersistedPolicy() (EffectivePolicy, bool) {
	if s.db == nil {
		return EffectivePolicy{}, false
	}
	db := s.db()
	if db == nil {
		return EffectivePolicy{}, false
	}

	if persisted, ok := s.readCompressedPolicy(db); ok {
		return persisted, true
	}

	data, err := db.CacheGetStale(CacheKey)
	if err != nil || len(data) == 0 {
		return EffectivePolicy{}, false
	}
	var persisted EffectivePolicy
	if err := json.Unmarshal(data, &persisted); err != nil {
		return EffectivePolicy{}, false
	}
	if len(persisted.Items) == 0 {
		return EffectivePolicy{}, false
	}

	// Migração best-effort para o formato comprimido. Só remove a chave antiga
	// (que ocupa ~31 MB no arquivo) depois de gravar a nova com sucesso.
	if err := s.persistCompressedPolicy(db, persisted); err == nil {
		if delErr := db.CacheDelete(CacheKey); delErr != nil {
			s.logf(fmt.Sprintf("aviso: falha ao remover cache legado da app-store: %v", delErr))
		} else {
			s.logf("app-store: cache persistido migrado para o formato comprimido (v2)")
		}
	} else {
		s.logf(fmt.Sprintf("aviso: falha ao migrar cache da app-store para v2: %v", err))
	}

	return persisted, true
}

// readCompressedPolicy lê e decodifica o cache v2 (gzip + JSON) sem
// materializar o JSON inteiro: o decoder consome o stream e só o objeto
// decodificado fica na memória.
func (s *Service) readCompressedPolicy(db *database.DB) (EffectivePolicy, bool) {
	data, err := db.CacheGetBytesStale(CacheKeyCompressed)
	if err != nil || len(data) == 0 {
		return EffectivePolicy{}, false
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return EffectivePolicy{}, false
	}
	var persisted EffectivePolicy
	decodeErr := json.NewDecoder(gz).Decode(&persisted)
	_ = gz.Close()
	if decodeErr != nil || len(persisted.Items) == 0 {
		return EffectivePolicy{}, false
	}
	return persisted, true
}

// persistCompressedPolicy grava a política efetiva comprimida (gzip) no cache
// do SQLite. O S TTL é o mesmo do formato antigo (SQLiteCacheTTL).
func (s *Service) persistCompressedPolicy(db *database.DB, policy EffectivePolicy) error {
	if db == nil {
		return fmt.Errorf("database indisponivel")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if err := json.NewEncoder(gz).Encode(policy); err != nil {
		_ = gz.Close()
		return fmt.Errorf("gzip encode: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("gzip close: %w", err)
	}
	return db.CacheSetBytes(CacheKeyCompressed, buf.Bytes(), SQLiteCacheTTL)
}

// persistedPolicyFresh avalia o frescor da política persistida pelo próprio
// FetchedAt (a entrada não tem TTL confiável — pode ter sido gravada com TTL
// expirado ou lida via CacheGetStale). FetchedAt ausente/ilegível é tratado
// como fresco (comportamento conservador compatível com a leitura antiga).
func (s *Service) persistedPolicyFresh(persisted EffectivePolicy) bool {
	if strings.TrimSpace(persisted.FetchedAt) == "" {
		return true
	}
	fetchedAt, err := time.Parse(time.RFC3339, persisted.FetchedAt)
	if err != nil {
		return true
	}
	return time.Since(fetchedAt) < SQLiteCacheTTL
}

// fetchEffectivePolicy baixa winget+chocolatey (custom tolerante), monta a
// política efetiva e persiste no cache de memória + SQLite.
func (s *Service) fetchEffectivePolicy(ctx context.Context) (EffectivePolicy, error) {
	results := make([]Response, 0, 3)
	for _, installationType := range []InstallationType{InstallationWinget, InstallationChocolatey} {
		payload, err := s.FetchByInstallationType(ctx, installationType)
		if err != nil {
			s.logf(fmt.Sprintf("falha ao carregar app-store (%s): %v", installationType, err))
			return EffectivePolicy{}, err
		}
		results = append(results, payload)
	}

	// Custom é fetch tolerante: o servidor ainda não publica metadados completos
	// de apps custom (hoje entrega só o packageId). Uma falha aqui não pode
	// derrubar winget/chocolatey — apenas loga e segue sem os itens custom.
	if customPayload, err := s.FetchByInstallationType(ctx, InstallationCustom); err != nil {
		s.logf(fmt.Sprintf("aviso: app-store (Custom) indisponivel (o servidor pode nao suportar ainda): %v", err))
	} else {
		results = append(results, customPayload)
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
	sortEffectiveItems(items)

	policy := EffectivePolicy{
		Items:     items,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.cache.Set(policy)
	if s.db != nil && s.db() != nil {
		if err := s.persistCompressedPolicy(s.db(), policy); err != nil {
			s.logf(fmt.Sprintf("aviso: falha ao salvar cache da app-store: %v", err))
		}
	}

	// Após (re)construir a política inteira — dezenas de MB transitórios no
	// caso de 27k itens — devolve as páginas livres ao SO. Rate-limited: nunca
	// acontece duas vezes em menos de 10 minutos, então não engasga cliques.
	memlimit.Relax()

	s.logf(fmt.Sprintf("app-store efetiva carregada: %d item(ns)", len(policy.Items)))
	return policy, nil
}

// GetCatalogFromAppStore converte a política efetiva em um catálogo (usa cache).
func (s *Service) GetCatalogFromAppStore(ctx context.Context) (models.Catalog, error) {
	return s.buildCatalogFromPolicy(ctx, false)
}

// RefreshCatalogFromAppStore monta o catálogo forçando um fetch novo da API
// (ignora o cache de memória). É usado pelo botão de atualizar da loja para
// refletir imediatamente pacotes custom recém-cadastrados/aprovados no servidor.
// Se o fetch falhar, o LoadEffectivePolicy serve o cache stale persistido — a
// loja continua utilizável offline.
func (s *Service) RefreshCatalogFromAppStore(ctx context.Context) (models.Catalog, error) {
	return s.buildCatalogFromPolicy(ctx, true)
}

// buildCatalogFromPolicy monta o catálogo a partir da política efetiva.
func (s *Service) buildCatalogFromPolicy(ctx context.Context, forceRefresh bool) (models.Catalog, error) {
	policy, err := s.LoadEffectivePolicy(ctx, forceRefresh)
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
			Icon:               absoluteAssetURL(s.getDebugConfig(), item.IconURL),
			// Origem do app para exibição na loja (Winget/Chocolatey/Custom) e
			// escopo da regra de aprovação (Global/Client/Site/Agent).
			InstallationType: strings.TrimSpace(item.InstallationType),
			SourceScope:      strings.TrimSpace(item.SourceScope),
			// Pacotes custom: tipo de comando e configuração de detecção
			// (contrato §1.2). Installed é preenchido fora daqui, por quem
			// tem acesso ao inventário local (inventory.MarkCatalogInstalled).
			InstallCommandKind:   item.InstallCommandKind,
			UninstallCommandKind: item.UninstallCommandKind,
			DetectionMode:        item.DetectionMode,
			InventorySoftwareID:  strings.TrimSpace(item.InventorySoftwareID),
			DetectionName:        strings.TrimSpace(item.DetectionName),
			DetectionPublisher:   strings.TrimSpace(item.DetectionPublisher),
			DetectionMinVersion:  strings.TrimSpace(item.DetectionMinVersion),
			DetectionCommand:     strings.TrimSpace(item.DetectionCommand),
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

// absoluteAssetURL resolve URLs relativas devolvidas pela API (ex.: o ícone
// de um pacote custom em /api/v1/app-store/custom/{id}/icon) para uma URL
// absoluta. Sem isso a tag <img> do WebView do agent resolve a URL contra a
// origem local do Wails e a imagem não carrega.
func absoluteAssetURL(cfg DebugConfig, raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || !strings.HasPrefix(value, "/") {
		return value
	}

	server := strings.TrimSpace(cfg.ApiServer)
	if server == "" {
		return value
	}

	scheme := strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return scheme + "://" + server + value
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
