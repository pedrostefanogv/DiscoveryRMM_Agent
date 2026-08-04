// Package uiruntime encapsula a sondagem do estado da UI nativa
// (watchdog system removido — mantido como stubs de plataforma).
package uiruntime

// NativeProbe representa o resultado da sondagem da UI nativa.
type NativeProbe struct {
	Supported   bool
	WindowFound bool
	Visible     bool
	Hung        bool
	Title       string
}
