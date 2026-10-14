//go:build !windows

package app

import "context"

// startUIWatchdog é um stub fora do Windows: o agente desktop e o serviço
// existem só lá (mesmo padrão de servicemode_service_other.go).
func (a *App) startUIWatchdog(_ context.Context) {}
