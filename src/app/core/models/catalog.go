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
}

// UpgradeItem represents a single package with a pending update.
type UpgradeItem struct {
	Name             string `json:"name"`
	ID               string `json:"id"`
	CurrentVersion   string `json:"currentVersion"`
	AvailableVersion string `json:"availableVersion"`
	Source           string `json:"source"`
}
