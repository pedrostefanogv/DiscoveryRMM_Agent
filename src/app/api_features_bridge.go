package app

import (
	"context"

	"discovery/app/apiclient"
)

// DetectApiFeatures testa a conectividade com a API e detecta quais features estão disponíveis.
// Delega para o apiclient.Service.
func (a *App) DetectApiFeatures(ctx context.Context) *apiclient.ApiVersionInfo {
	if a == nil || a.ApiClientSvc == nil {
		return &apiclient.ApiVersionInfo{Features: make([]string, 0)}
	}
	return a.ApiClientSvc.DetectApiFeatures(ctx)
}
