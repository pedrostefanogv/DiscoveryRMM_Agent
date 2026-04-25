//go:build windows

package main

// suppressGameBarOverlay não altera mais o GameConfigStore.
// Observamos que o Windows usa entradas internas com formato diferente
// (GUIDs e metadados próprios), então as chaves ad-hoc criadas pelo app não
// impediam o Xbox Game Bar de classificar o processo como jogo.
func suppressGameBarOverlay() string {
	return "supressao via GameConfigStore desativada; use --window-frame=standard ou DISCOVERY_WINDOW_FRAME=standard para diagnosticar heuristicas do Xbox Game Bar"
}
