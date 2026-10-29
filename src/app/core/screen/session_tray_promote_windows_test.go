//go:build windows

package screen

import "testing"

func TestNotifyIconPathMatches(t *testing.T) {
	exe := `C:\Program Files\Discovery\discovery-service.exe`
	cases := []struct {
		name      string
		registry  string
		exe       string
		wantMatch bool
	}{
		{"literal", exe, exe, true},
		{"knownfolder", `{6D809377-6AF0-444B-8957-A3773F02200E}\Discovery\discovery-service.exe`, exe, true},
		{"case insensitive", `{6D809377-6AF0-444B-8957-A3773F02200E}\Discovery\DISCOVERY-SERVICE.EXE`, exe, true},
		{"barras invertidas", `{6D809377-6AF0-444B-8957-A3773F02200E}/Discovery/discovery-service.exe`, exe, true},
		{"outro executavel", `{6D809377-6AF0-444B-8957-A3773F02200E}\Discovery\discovery-agent.exe`, exe, false},
		{"vazio", "", exe, false},
		{"sufixo parcial", `service.exe`, exe, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := notifyIconPathMatches(tc.registry, tc.exe); got != tc.wantMatch {
				t.Fatalf("notifyIconPathMatches(%q, %q)=%v want %v", tc.registry, tc.exe, got, tc.wantMatch)
			}
		})
	}
}
