package platform

import (
	"runtime"
	"testing"
)

func TestDataDir_NotEmpty(t *testing.T) {
	result := DataDir()
	if result == "" {
		t.Fatal("DataDir retornou vazio")
	}
	if result == "." && runtime.GOOS == "windows" {
		t.Fatal("DataDir retornou '.' em Windows — deveria retornar ProgramData ou LOCALAPPDATA")
	}
}

func TestConfigPathCandidates_NotEmpty(t *testing.T) {
	candidates := ConfigPathCandidates()
	if len(candidates) == 0 {
		t.Fatal("ConfigPathCandidates retornou lista vazia")
	}
	for _, path := range candidates {
		if !contains(path, "config.json") {
			t.Errorf("candidato nao termina com config.json: %s", path)
		}
	}
}

func TestInstallerOverridePathCandidates_NotEmpty(t *testing.T) {
	candidates := InstallerOverridePathCandidates()
	if len(candidates) == 0 {
		t.Fatal("InstallerOverridePathCandidates retornou lista vazia")
	}
	for _, path := range candidates {
		if !contains(path, "installer.json") {
			t.Errorf("candidato nao termina com installer.json: %s", path)
		}
	}
}

func TestSharedConfigPath_NotEmpty(t *testing.T) {
	path := SharedConfigPath()
	if path == "" {
		t.Fatal("SharedConfigPath retornou vazio")
	}
	if !contains(path, "config.json") {
		t.Errorf("SharedConfigPath nao termina com config.json: %s", path)
	}
}

func TestTempDir_NotEmpty(t *testing.T) {
	result := TempDir()
	if result == "" {
		t.Fatal("TempDir retornou vazio")
	}
}

func TestP2PTempDir_NotEmpty(t *testing.T) {
	result := P2PTempDir()
	if result == "" {
		t.Fatal("P2PTempDir retornou vazio")
	}
}

func TestMeshAgentPathCandidates_HasPaths(t *testing.T) {
	candidates := MeshAgentPathCandidates()
	if len(candidates) == 0 {
		t.Fatal("MeshAgentPathCandidates retornou lista vazia")
	}
}

func TestChatConfigPathCandidates_NotEmpty(t *testing.T) {
	candidates := ChatConfigPathCandidates("chat_config.json")
	if len(candidates) == 0 {
		t.Fatal("ChatConfigPathCandidates retornou lista vazia")
	}
	for _, path := range candidates {
		if !contains(path, "chat_config.json") {
			t.Errorf("candidato nao termina com chat_config.json: %s", path)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[len(s)-len(substr):] == substr
}
