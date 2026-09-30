//go:build !windows

package screenshot

import "discovery/app/core/screen"

func selfPID() int { return 0 }

// toneMapIfHDR é no-op fora do Windows (captura não suportada).
func toneMapIfHDR(f *screen.Frame) *screen.Frame { return f }
