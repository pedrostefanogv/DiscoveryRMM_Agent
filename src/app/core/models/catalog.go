package models

type Catalog struct {
	Generated        string    `json:"generated"`
	Count            int       `json:"count"`
	PackagesWithIcon int       `json:"packagesWithIcon"`
	Packages         []AppItem `json:"packages"`
}

type AppItem struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Publisher      string   `json:"publisher"`
	Version        string   `json:"version"`
	Description    string   `json:"description"`
	Homepage       string   `json:"homepage"`
	License        string   `json:"license"`
	Tags           []string `json:"tags"`
	InstallCommand string   `json:"installCommand"`
	// SilentCommand contém os switches silenciosos vindos do catálogo
	// (ex.: "/S /PreventRebootRequired=true"). Fallback: SilentWithProgress.
	SilentCommand      string `json:"silent,omitempty"`
	SilentWithProgress string `json:"silentWithProgress"`
	Category           string `json:"category"`
	Icon               string `json:"icon"`
	LastUpdated        string `json:"lastUpdated"`
	// InstallationType é a origem do app na loja: "Winget", "Chocolatey" ou
	// "Custom" (cadastro manual no servidor). Usado pela UI para o badge de
	// origem nos cards e no modal de detalhe.
	InstallationType string `json:"installationType"`
	// SourceScope é o escopo da regra de aprovação que liberou o app
	// (Global/Client/Site/Agent). Vazio quando o servidor não informa.
	SourceScope string `json:"sourceScope"`

	// ── Pacotes custom: tipo de comando e detecção (contrato §1.2) ──
	// InstallCommandKind/UninstallCommandKind: 0 = comando único (cmd /C),
	// 1 = script CMD/BAT, 2 = script PowerShell.
	InstallCommandKind   int `json:"installCommandKind"`
	UninstallCommandKind int `json:"uninstallCommandKind"`
	// DetectionMode: 0 = nenhuma, 1 = vinculado a app do inventário,
	// 2 = comando de detecção (PowerShell, exit 0 = instalado).
	DetectionMode       int    `json:"detectionMode"`
	InventorySoftwareID string `json:"inventorySoftwareId,omitempty"`
	DetectionName       string `json:"detectionName,omitempty"`
	DetectionPublisher  string `json:"detectionPublisher,omitempty"`
	DetectionMinVersion string `json:"detectionMinVersion,omitempty"`
	DetectionCommand    string `json:"detectionCommand,omitempty"`
	// Installed é um campo de RUNTIME (não vem da API): indica que o app custom
	// já foi detectado como instalado no host pela detecção configurada.
	// Alimenta o badge "Já instalado" e o rótulo "Reinstalar" na loja.
	Installed bool `json:"installed"`
}

// UpgradeItem represents a single package with a pending update.
type UpgradeItem struct {
	Name             string `json:"name"`
	ID               string `json:"id"`
	CurrentVersion   string `json:"currentVersion"`
	AvailableVersion string `json:"availableVersion"`
	Source           string `json:"source"`
}
