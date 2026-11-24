//go:build windows

package native

import (
	"math"
	"testing"
)

func TestWmiFloat_HandlesFloat32AndIntegers(t *testing.T) {
	row := map[string]any{
		"r4":  float32(61.5),
		"r8":  float64(54.2),
		"i64": int64(3180),
		"u32": uint32(318),
		"str": "45.5",
	}

	if got := wmiFloat(row, "r4"); math.Abs(got-61.5) > 0.01 {
		t.Fatalf("wmiFloat(float32) = %v, want 61.5", got)
	}
	if got := wmiFloat(row, "r8"); math.Abs(got-54.2) > 0.01 {
		t.Fatalf("wmiFloat(float64) = %v, want 54.2", got)
	}
	if got := wmiFloat(row, "i64"); got != 3180 {
		t.Fatalf("wmiFloat(int64) = %v, want 3180", got)
	}
	if got := wmiFloat(row, "u32"); got != 318 {
		t.Fatalf("wmiFloat(uint32) = %v, want 318", got)
	}
	if got := wmiFloat(row, "str"); math.Abs(got-45.5) > 0.01 {
		t.Fatalf("wmiFloat(string) = %v, want 45.5", got)
	}
}
