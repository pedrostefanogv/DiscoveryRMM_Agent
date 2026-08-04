package app

import (
	"context"

	"discovery/app/apiclient"
)

// DetectApiFeatures testa a conectividade com a API e detecta quais features estão disponíveis.
// Delega para o apiclient.Service.
func (a *App) DetectApiFeatures(ctx context.Context) *apiclient.ApiVersionInfo {
	if a == nil || a.apiClientSvc == nil {
		return &apiclient.ApiVersionInfo{Features: make([]string, 0)}
	}
	return a.apiClientSvc.DetectApiFeatures(ctx)
}
