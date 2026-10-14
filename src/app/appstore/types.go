package appstore

import (
	"sync"
	"time"
)

// InstallationType representa os tipos suportados no app-store do agent.
type InstallationType string

const (
	InstallationWinget     InstallationType = "Winget"
	InstallationChocolatey InstallationType = "Chocolatey"
	// InstallationCustom representa apps cadastrados manualmente no servidor
	// (AppInstallationType.Custom da API). O servidor ainda entrega apenas o
	// packageId para esse tipo — metadados/comando são melhorias futuras do
	// servidor; o agent já está preparado para exibir e instalar.
	InstallationCustom InstallationType = "Custom"
)

// Item representa um item permitido retornado por /api/v1/agent-auth/me/app-store.
type Item struct {
	InstallationType string `json:"installationType"`
	PackageID        string `json:"packageId"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	IconURL          string `json:"iconUrl"`
	Publisher        string `json:"publisher"`
	Version          string `json:"version"`
	InstallCommand   string `json:"installCommand"`
	// UninstallCommand é o comando de remoção de apps custom (cadastro manual).
	UninstallCommand string `json:"uninstallCommand"`
	// ExtractZip indica que o arquivo do pacote é um ZIP e deve ser extraído
	// no host antes de executar o comando de instalação.
	ExtractZip bool `json:"extractZip"`
	// FileName é o nome original do instalador publicado pelo servidor.
	FileName string `json:"fileName"`
	// FileSizeBytes é o tamanho do instalador em bytes (0 quando desconhecido).
	FileSizeBytes int64 `json:"fileSizeBytes"`
	// FileURL é a URL (relativa) do instalador no servidor; o agent baixa com o
	// próprio token de autenticação. Vazia quando o pacote não tem arquivo.
	FileURL string `json:"fileUrl"`
	// FileChecksum é o SHA-256 (hex) do instalador. Quando presente, o agent
	// valida a integridade do arquivo baixado antes de executá-lo.
	FileChecksum string `json:"fileChecksum"`
	// SilentCommand contém os switches silenciosos do manifesto winget
	// (ex.: "/S /PreventRebootRequired=true"). Fallback: SilentWithProgress.
	SilentCommand string `json:"silent"`
	// SilentWithProgress contém os switches silenciosos com progresso (fallback de SilentCommand).
	SilentWithProgress  string            `json:"silentWithProgress"`
	InstallerURLsByArch map[string]string `json:"installerUrlsByArch"`
	// InstallerTypesByArch contém o InstallerType do manifesto winget por
	// arquitetura (ex.: "wix", "burn", "nullsoft", "inno", "zip", "portable").
	// Permite executar o instalador baixado com a estratégia correta sem
	// adivinhar pela extensão do arquivo.
	InstallerTypesByArch map[string]string `json:"installerTypesByArch"`
	AutoUpdateEnabled    bool              `json:"autoUpdateEnabled"`
	SourceScope          string            `json:"sourceScope"`

	// ── Pacotes custom: tipo de comando e detecção (contrato §1.2) ──
	// InstallCommandKind/UninstallCommandKind dizem como o texto do comando
	// deve ser executado: Command (0) = cmd /C; CmdScript (1) = texto gravado
	// num .cmd; PowerShellScript (2) = texto gravado num .ps1.
	InstallCommandKind   int `json:"installCommandKind"`
	UninstallCommandKind int `json:"uninstallCommandKind"`
	// DetectionMode: None (0) = sem detecção; InventoryLink (1) = casar com o
	// inventário local por nome/fabricante/versão mínima; Command (2) =
	// executar DetectionCommand (exit 0 = já instalado).
	DetectionMode int `json:"detectionMode"`
	// InventorySoftwareID é o softwareId (Guid) do catálogo de inventário do
	// servidor. Snapshot informativo — o agent casa pelo nome/fabricante.
	InventorySoftwareID string `json:"inventorySoftwareId"`
	// DetectionName é o nome do app no inventário (obrigatório no modo
	// InventoryLink) e DetectionPublisher o fabricante (opcional).
	DetectionName      string `json:"detectionName"`
	DetectionPublisher string `json:"detectionPublisher"`
	// DetectionMinVersion exige versão instalada >= este valor (opcional).
	DetectionMinVersion string `json:"detectionMinVersion"`
	// DetectionCommand é o PowerShell de detecção (exit 0 = instalado).
	DetectionCommand string `json:"detectionCommand"`
}

// AppCommandKind espelha AppCommandKind da API (contrato §1.1): como o texto
// do comando de instalação/desinstalação de um pacote custom é executado.
const (
	CommandKindCommand          = 0
	CommandKindCmdScript        = 1
	CommandKindPowerShellScript = 2
)

// AppDetectionMode espelha AppDetectionMode da API (contrato §1.1).
const (
	DetectionModeNone          = 0
	DetectionModeInventoryLink = 1
	DetectionModeCommand       = 2
)

// Response representa o envelope do endpoint /api/v1/agent-auth/me/app-store.
// Suporta paginação CQRS por cursor — quando nextCursor não está vazio,
// o caller deve fazer nova requisição com ?cursor=<nextCursor> para obter
// a próxima página.
type Response struct {
	InstallationType string `json:"installationType"`
	Count            int    `json:"count"`
	Items            []Item `json:"items"`
	NextCursor       string `json:"nextCursor"`
	HasMore          bool   `json:"hasMore"`
}

// EffectivePolicy consolida os itens permitidos para todos os tipos suportados.
type EffectivePolicy struct {
	Items     []Item `json:"items"`
	FetchedAt string `json:"fetchedAt"`
}

type PolicyCache struct {
	mu       sync.RWMutex
	policy   EffectivePolicy
	loadedAt time.Time
	loaded   bool
}

func (c *PolicyCache) Get(maxAge time.Duration) (EffectivePolicy, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return EffectivePolicy{}, false
	}
	if maxAge > 0 && time.Since(c.loadedAt) > maxAge {
		return EffectivePolicy{}, false
	}
	return c.policy, true
}

func (c *PolicyCache) get(maxAge time.Duration) (EffectivePolicy, bool) {
	return c.Get(maxAge)
}

func (c *PolicyCache) Set(policy EffectivePolicy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policy = policy
	c.loadedAt = time.Now()
	c.loaded = true
}

func (c *PolicyCache) set(policy EffectivePolicy) {
	c.Set(policy)
}

func (c *PolicyCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policy = EffectivePolicy{}
	c.loadedAt = time.Time{}
	c.loaded = false
}

func (c *PolicyCache) invalidate() {
	c.Invalidate()
}
