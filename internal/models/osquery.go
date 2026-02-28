package models

type OsqueryStatus struct {
	Installed          bool   `json:"installed"`
	Path               string `json:"path"`
	SuggestedPackageID string `json:"suggestedPackageID"`
}
