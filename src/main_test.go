package main

import "testing"

func TestEnforceStartupMinimized(t *testing.T) {
	tests := []struct {
		name          string
		source        string
		minimized     bool
		wantSource    string
		wantMinimized bool
	}{
		{
			name:          "update-restart sem flag força minimized",
			source:        "update-restart",
			minimized:     false,
			wantSource:    "update-restart",
			wantMinimized: true,
		},
		{
			name:          "update-restart com flag mantém minimized",
			source:        "update-restart",
			minimized:     true,
			wantSource:    "update-restart",
			wantMinimized: true,
		},
		{
			name:          "autostart sem flag força minimized",
			source:        "autostart",
			minimized:     false,
			wantSource:    "autostart",
			wantMinimized: true,
		},
		{
			name:          "task-scheduler sem flag força minimized",
			source:        "task-scheduler",
			minimized:     false,
			wantSource:    "task-scheduler",
			wantMinimized: true,
		},
		{
			name:          "startup-link sem flag força minimized",
			source:        "startup-link",
			minimized:     false,
			wantSource:    "startup-link",
			wantMinimized: true,
		},
		{
			name:          "manual não é alterado",
			source:        "manual",
			minimized:     false,
			wantSource:    "manual",
			wantMinimized: false,
		},
		{
			name:          "origem desconhecida não é alterada",
			source:        "custom-source",
			minimized:     false,
			wantSource:    "custom-source",
			wantMinimized: false,
		},
		{
			name:          "casing variado é tratado (normalização feita antes)",
			source:        "Update-Restart",
			minimized:     false,
			wantSource:    "Update-Restart",
			wantMinimized: false, // função pura não normaliza; main.go normaliza antes
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSource, gotMinimized := enforceStartupMinimized(tt.source, tt.minimized)
			if gotSource != tt.wantSource {
				t.Errorf("source = %q, want %q", gotSource, tt.wantSource)
			}
			if gotMinimized != tt.wantMinimized {
				t.Errorf("minimized = %v, want %v", gotMinimized, tt.wantMinimized)
			}
		})
	}
}
