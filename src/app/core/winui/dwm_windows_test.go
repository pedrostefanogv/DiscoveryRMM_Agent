//go:build windows

package winui

import "testing"

// Sem janela válida o helper não deve chamar o DWM nem quebrar: o chamador cai
// no fallback de região (SetRoundedRegion).
func TestSetSystemRoundedCornersRejectsInvalidWindow(t *testing.T) {
	if SetSystemRoundedCorners(0) {
		t.Fatal("SetSystemRoundedCorners(0) deveria devolver false")
	}
}
