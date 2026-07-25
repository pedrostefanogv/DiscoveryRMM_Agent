package app

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/samber/lo"

	"discovery/app/debug"
	"discovery/internal/platform"
)

func installerConfigPathCandidates() []string {
	return platform.ConfigPathCandidates()
}

func installerOverridePathCandidates() []string {
	return platform.InstallerOverridePathCandidates()
}

func loadInstallerConfigFromCandidates(paths []string) (InstallerConfig, string, bool, error) {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				log.Printf("[config] aviso: não foi possível ler config candidate %s: %v", path, err)
			}
			continue
		}

		var cfg InstallerConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			return InstallerConfig{}, "", false, fmt.Errorf("falha ao ler %s: %w", path, err)
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
		cfg.P2P = normalizeP2PConfig(cfg.P2P)

		return cfg, path, true, nil
	}

	return InstallerConfig{}, "", false, nil
}

func mergeInstallerOverride(base, override InstallerConfig) InstallerConfig {
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

func findInstallerOverridePath() string {
	for _, path := range installerOverridePathCandidates() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func cleanupLegacyInstallerOverrideFiles() {
	for _, path := range installerOverridePathCandidates() {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			continue
		}
	}
}

func installerConfigWriteCandidates(sourcePath string) []string {
	paths := make([]string, 0, len(platform.ConfigPathCandidates())+1)
	paths = append(paths, platform.ConfigPathCandidates()...)
	if strings.TrimSpace(sourcePath) != "" {
		paths = append(paths, sourcePath)
	}
	return lo.Uniq(paths)
}

func loadInstallerConfig() (InstallerConfig, string, error) {
	baseCfg, basePath, baseFound, baseErr := loadInstallerConfigFromCandidates(installerConfigPathCandidates())
	if baseErr != nil {
		return InstallerConfig{}, "", baseErr
	}

	overrideCfg, _, overrideFound, overrideErr := loadInstallerConfigFromCandidates(installerOverridePathCandidates())
	if overrideErr != nil {
		return InstallerConfig{}, "", overrideErr
	}

	// Só cria config padrão quando NENHUM arquivo existe.
	if !baseFound && !overrideFound {
		return ensureDefaultInstallerConfig()
	}

	resolved := baseCfg
	resolvedPath := basePath

	if !baseFound {
		// Só override está presente.
		resolved = overrideCfg
		resolvedPath = ""
	} else {
		resolved = mergeInstallerOverride(baseCfg, overrideCfg)
	}

	// Se o config não tem conexão (sem ApiServer, ServerAPI, ServerURL nem deployToken),
	// retorna como está — NÃO sobrescreve com defaults. O bootstrap tratará.
	if resolved.ApiServer == "" && resolved.ServerAPI == "" && resolved.ServerURL == "" && resolved.APIKey == "" {
		log.Printf("[config] config.json encontrado mas sem conexão — retornando como está para bootstrap")
		return resolved, resolvedPath, nil
	}

	if overrideFound {
		if writePath, err := persistInstallerConfig(resolvedPath, resolved); err == nil {
			return resolved, writePath, nil
		}
	}

	return resolved, resolvedPath, nil
}

// ensureDefaultInstallerConfig cria um config.json padrão com P2P ativo
// quando nenhum arquivo de configuração é encontrado. Isso garante que
// o agente possa iniciar em modo zero-touch mesmo após um update onde
// o NSIS pulou a criação do config (modo /UPDATE).
//
// NUNCA sobrescreve um arquivo existente — apenas cria quando o caminho
// não existe no disco.
//
// O JSON gerado é mínimo: apenas autoProvisioning, p2p.enabled e chatLog.enabled.
// Campos como deployToken, authToken, agentId, siteId, clientId e detalhes de P2P
// NÃO são incluídos — o agente dependerá exclusivamente de zero-touch P2P para
// obter essas informações no primeiro provisionamento.
func ensureDefaultInstallerConfig() (InstallerConfig, string, error) {
	autoProv := true
	chatLogEnabled := true
	cfg := InstallerConfig{
		AutoProvisioning: &autoProv,
		P2P: P2PConfig{
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

	// Serializa apenas os campos mínimos para evitar poluir o config.json
	// com dezenas de campos zerados do struct p2pmeta.Config (que não usa
	// omitempty para a maioria dos campos).
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

func persistInstallerConfig(sourcePath string, cfg InstallerConfig) (string, error) {
	paths := installerConfigWriteCandidates(sourcePath)
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
		cleanupLegacyInstallerOverrideFiles()
		return path, nil
	}

	return "", fmt.Errorf("falha ao persistir config de producao: %s", strings.Join(errs, " | "))
}
