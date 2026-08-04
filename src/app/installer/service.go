// Package installer encapsula a leitura/persistência da configuração do
// instalador (config.json / installer.json), separado do App.
package installer

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/samber/lo"

	"discovery/app/core/platform"
	"discovery/app/debug"
	"discovery/app/p2pmeta"
)

// Config é um alias para debug.InstallerConfig.
type Config = debug.InstallerConfig

// Deps são as dependências injetadas no Service.
type Deps struct {
	// NormalizeP2PConfig normaliza a configuração P2P.
	NormalizeP2PConfig func(p2pmeta.Config) p2pmeta.Config
}

// Service encapsula a leitura/persistência da configuração do instalador.
type Service struct {
	normalizeP2PConfig func(p2pmeta.Config) p2pmeta.Config
}

// New cria um InstallerService.
func New(deps Deps) *Service {
	return &Service{normalizeP2PConfig: deps.NormalizeP2PConfig}
}

// ConfigPathCandidates retorna os caminhos candidatos do config.
func (s *Service) ConfigPathCandidates() []string {
	return platform.ConfigPathCandidates()
}

// OverridePathCandidates retorna os caminhos candidatos do installer.json.
func (s *Service) OverridePathCandidates() []string {
	return platform.InstallerOverridePathCandidates()
}

func (s *Service) loadFromCandidates(paths []string) (Config, string, bool, error) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("[config] aviso: não foi possível ler config candidate %s: %v", path, err)
			}
			continue
		}

		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Config{}, "", false, fmt.Errorf("falha ao ler %s: %w", path, err)
		}

		cfg.ServerURL = strings.TrimSpace(cfg.ServerURL)
		cfg.ServerAPI = strings.TrimSpace(cfg.ServerAPI)
		cfg.APIKey = strings.TrimSpace(cfg.APIKey)
		cfg.ApiScheme = strings.TrimSpace(strings.ToLower(cfg.ApiScheme))
		cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
		cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
		cfg.AgentID = strings.TrimSpace(cfg.AgentID)
		cfg.ClientID = strings.TrimSpace(cfg.ClientID)
		cfg.SiteID = strings.TrimSpace(cfg.SiteID)
		if s.normalizeP2PConfig != nil {
			cfg.P2P = s.normalizeP2PConfig(cfg.P2P)
		}

		return cfg, path, true, nil
	}

	return Config{}, "", false, nil
}

func (s *Service) mergeOverride(base, override Config) Config {
	if strings.TrimSpace(override.ServerURL) != "" {
		base.ServerURL = strings.TrimSpace(override.ServerURL)
	}
	if strings.TrimSpace(override.ServerAPI) != "" {
		base.ServerAPI = strings.TrimSpace(override.ServerAPI)
	}
	if strings.TrimSpace(override.APIKey) != "" {
		base.APIKey = strings.TrimSpace(override.APIKey)
	}
	if strings.TrimSpace(override.ApiScheme) != "" {
		base.ApiScheme = strings.TrimSpace(strings.ToLower(override.ApiScheme))
	}
	if strings.TrimSpace(override.ApiServer) != "" {
		base.ApiServer = strings.TrimSpace(override.ApiServer)
	}
	if override.AutoProvisioning != nil {
		base.AutoProvisioning = override.AutoProvisioning
	}
	if override.AllowInsecureTLS != nil {
		base.AllowInsecureTLS = override.AllowInsecureTLS
	}
	if strings.TrimSpace(override.ClientID) != "" {
		base.ClientID = strings.TrimSpace(override.ClientID)
	}
	if strings.TrimSpace(override.SiteID) != "" {
		base.SiteID = strings.TrimSpace(override.SiteID)
	}
	return base
}

func (s *Service) findOverridePath() string {
	for _, path := range s.OverridePathCandidates() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func (s *Service) cleanupLegacyOverrideFiles() {
	for _, path := range s.OverridePathCandidates() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			continue
		}
	}
}

func (s *Service) writeCandidates(sourcePath string) []string {
	paths := make([]string, 0, len(platform.ConfigPathCandidates())+1)
	paths = append(paths, platform.ConfigPathCandidates()...)
	if strings.TrimSpace(sourcePath) != "" {
		paths = append(paths, sourcePath)
	}
	return lo.Uniq(paths)
}

// LoadFromCandidates carrega a config a partir de caminhos candidatos.
func (s *Service) LoadFromCandidates(paths []string) (Config, string, bool, error) {
	return s.loadFromCandidates(paths)
}

// MergeOverride mescla a config base com o override.
func (s *Service) MergeOverride(base, override Config) Config {
	return s.mergeOverride(base, override)
}

// FindOverridePath encontra o caminho do override.
func (s *Service) FindOverridePath() string {
	return s.findOverridePath()
}

// CleanupLegacyOverrideFiles limpa arquivos de override legados.
func (s *Service) CleanupLegacyOverrideFiles() {
	s.cleanupLegacyOverrideFiles()
}

// WriteCandidates retorna os caminhos candidatos de escrita.
func (s *Service) WriteCandidates(sourcePath string) []string {
	return s.writeCandidates(sourcePath)
}

// EnsureDefault cria a config padrão quando nenhum arquivo existe.
func (s *Service) EnsureDefault() (Config, string, error) {
	return s.ensureDefault()
}

// Load carrega a configuração do instalador.
func (s *Service) Load() (Config, string, error) {
	baseCfg, basePath, baseFound, baseErr := s.loadFromCandidates(s.ConfigPathCandidates())
	if baseErr != nil {
		return Config{}, "", baseErr
	}

	overrideCfg, _, overrideFound, overrideErr := s.loadFromCandidates(s.OverridePathCandidates())
	if overrideErr != nil {
		return Config{}, "", overrideErr
	}

	// Só cria config padrão quando NENHUM arquivo existe.
	if !baseFound && !overrideFound {
		return s.ensureDefault()
	}

	resolved := baseCfg
	resolvedPath := basePath

	if !baseFound {
		// Só override está presente.
		resolved = overrideCfg
		resolvedPath = ""
	} else {
		resolved = s.mergeOverride(baseCfg, overrideCfg)
	}

	// Se o config não tem conexão (sem ApiServer, ServerAPI, ServerURL nem deployToken),
	// retorna como está — NÃO sobrescreve com defaults. O bootstrap tratará.
	if resolved.ApiServer == "" && resolved.ServerAPI == "" && resolved.ServerURL == "" && resolved.APIKey == "" {
		log.Printf("[config] config.json encontrado mas sem conexão — retornando como está para bootstrap")
		return resolved, resolvedPath, nil
	}

	if overrideFound {
		if writePath, err := s.Persist(resolvedPath, resolved); err == nil {
			return resolved, writePath, nil
		}
	}

	return resolved, resolvedPath, nil
}

// ensureDefault cria um config.json padrão com P2P ativo quando nenhum arquivo existe.
func (s *Service) ensureDefault() (Config, string, error) {
	autoProv := true
	chatLogEnabled := true
	cfg := Config{
		AutoProvisioning: &autoProv,
		P2P: p2pmeta.Config{
			Enabled: true,
		},
		ChatLog: debug.ChatLogConfig{
			Enabled: &chatLogEnabled,
		},
	}

	path := platform.SharedConfigPath()

	// Não sobrescreve arquivo existente — apenas cria se não existir.
	if _, err := os.Stat(path); err == nil {
		log.Printf("[config] config.json ja existe em %s — nao sera sobrescrito", path)
		return cfg, path, nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[config] aviso: nao foi possivel criar diretorio padrao %s: %v", dir, err)
		return cfg, "", nil // Retorna sem persistir, mas não falha
	}

	// Serializa apenas os campos mínimos para evitar poluir o config.json.
	minimal := map[string]any{
		"autoProvisioning": true,
		"p2p": map[string]any{
			"enabled": true,
		},
		"chatLog": map[string]any{
			"enabled": true,
		},
	}
	data, err := json.MarshalIndent(minimal, "", "  ")
	if err != nil {
		log.Printf("[config] aviso: nao foi possivel serializar config padrao: %v", err)
		return cfg, "", nil
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("[config] aviso: nao foi possivel persistir config padrao em %s: %v", path, err)
		return cfg, "", nil
	}

	log.Printf("[config] config padrao criado com auto-provisioning e P2P ativos em %s", path)
	log.Printf("[config] ATENCAO: config padrao criado sem deployToken. O agente dependera exclusivamente de zero-touch P2P para provisionamento.")
	return cfg, path, nil
}

// Persist persiste a configuração do instalador.
func (s *Service) Persist(sourcePath string, cfg Config) (string, error) {
	paths := s.writeCandidates(sourcePath)
	if len(paths) == 0 {
		return "", fmt.Errorf("nenhum caminho disponivel para persistir config de producao")
	}

	debug.CleanInstallerConfigForPersistence(&cfg)

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("falha ao serializar config de producao: %w", err)
	}

	var errs []string
	for _, path := range paths {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			errs = append(errs, dir+": "+err.Error())
			continue
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			errs = append(errs, path+": "+err.Error())
			continue
		}
		s.cleanupLegacyOverrideFiles()
		return path, nil
	}

	return "", fmt.Errorf("falha ao persistir config de producao: %s", strings.Join(errs, " | "))
}
