//go:build windows

package platform

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProcessIntegrityLevelAndReportUnderRace(t *testing.T) {
	il := ProcessIntegrityLevel()
	if il == IntegrityUnknown {
		t.Fatalf("ProcessIntegrityLevel() returned IntegrityUnknown")
	}
	if got := TokenIntegrityLevel(windows.GetCurrentProcessToken()); got != il {
		t.Fatalf("TokenIntegrityLevel(current) = %s, want %s", got, il)
	}
	if report := ElevationReport(); report == "" {
		t.Fatal("ElevationReport() returned empty string")
	}
}

func TestParseIntegrityRIDFromSIDBytes(t *testing.T) {
	buildSID := func(rev byte, subs ...uint32) []byte {
		buf := make([]byte, 8+len(subs)*4)
		buf[0] = rev
		buf[1] = byte(len(subs))
		buf[7] = 16 // SECURITY_MANDATORY_LABEL_AUTHORITY
		for i, sub := range subs {
			binary.LittleEndian.PutUint32(buf[8+i*4:12+i*4], sub)
		}
		return buf
	}

	cases := []struct {
		name string
		raw  []byte
		want IntegrityLevel
		ok   bool
	}{
		{"system", buildSID(1, 0x4000), IntegritySystem, true},
		{"high", buildSID(1, 0x3000), IntegrityHigh, true},
		{"medium", buildSID(1, 0x2000), IntegrityMedium, true},
		{"low", buildSID(1, 0x1000), IntegrityLow, true},
		{"untrusted", buildSID(1, 0x0001), IntegrityUntrusted, true},
		{"multi_subauth_reads_final_rid", buildSID(1, 0x1000, 0x2000, 0x3000), IntegrityHigh, true},
		{"zero_rid", buildSID(1, 0x0000), IntegrityUnknown, true},
		{"bad_revision", buildSID(2, 0x3000), IntegrityUnknown, false},
		{"zero_subauth", buildSID(1), IntegrityUnknown, false},
		{"truncated", []byte{1, 1, 0, 0, 0, 0, 0, 16, 0, 0}, IntegrityUnknown, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rid, ok := parseIntegrityRIDFromSIDBytes(tc.raw)
			if ok != tc.ok {
				t.Fatalf("ok = %t, want %t", ok, tc.ok)
			}
			if ok {
				if got := integrityLevelFromRID(rid); got != tc.want {
					t.Fatalf("integrityLevelFromRID(%#x) = %s, want %s", rid, got, tc.want)
				}
			}
		})
	}
}
