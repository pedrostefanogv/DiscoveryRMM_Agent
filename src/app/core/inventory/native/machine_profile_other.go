//go:build !windows

package native

import (
	"context"

	"discovery/app/core/models"
)

// collectMachineProfileNative não tem implementação fora do Windows: o perfil
// fica "desconhecido" (nunca "desktop").
func collectMachineProfileNative(_ context.Context, _ bool) models.MachineProfile {
	return models.MachineProfile{MachineType: models.MachineTypeUnknown}
}
