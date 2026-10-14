package app

import "discovery/app/core/runtimediag"

// GetRuntimeMemory devolve a fotografia atual de memória do runtime Go.
//
// Exposto para diagnóstico local (bridge Wails/IPC e chat de suporte): antes
// desta função não havia como obter heap/GC do processo em execução. É barato
// (runtime.ReadMemStats) e não altera nenhum estado.
func (a *App) GetRuntimeMemory() runtimediag.Snapshot {
	return runtimediag.Take()
}
