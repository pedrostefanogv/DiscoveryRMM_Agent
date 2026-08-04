package app

import (
	"context"
)

// UpsertCollectedCustomField envia um campo customizado coletado para o servidor.
// Delega para o customfields.Service.
func (a *App) UpsertCollectedCustomField(ctx context.Context, name, value, scope string) error {
	if a == nil || a.customFieldsSvc == nil {
		return nil
	}
	return a.customFieldsSvc.UpsertCollectedCustomField(ctx, name, value, scope)
}
