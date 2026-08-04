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
	Category       string   `json:"category"`
	Icon           string   `json:"icon"`
	LastUpdated    string   `json:"lastUpdated"`
}

// UpgradeItem represents a single package with a pending update.
type UpgradeItem struct {
	Name             string `json:"name"`
	ID               string `json:"id"`
	CurrentVersion   string `json:"currentVersion"`
	AvailableVersion string `json:"availableVersion"`
	Source           string `json:"source"`
}
