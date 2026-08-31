package p2p

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// newRegisterTestCoordinator cria um Coordinator com mockDeps apontando para
// um temp dir real (necessário para RegisterArtifactIDForFile).
func newRegisterTestCoordinator(t *testing.T) (*Coordinator, string) {
	t.Helper()
	dir := t.TempDir()
	c := &Coordinator{
		deps:        &mockDeps{tempDir: dir},
		sha256Cache: make(map[string]artifactSHA256CacheEntry),
	}
	return c, dir
}

func TestRegisterArtifactIDForFile_Success(t *testing.T) {
	c, dir := newRegisterTestCoordinator(t)

	// Simula o instalador do selfupdate já no P2P_Temp com nome canônico.
	filePath := filepath.Join(dir, "selfupdate-abc123.exe")
	if err := os.WriteFile(filePath, []byte("fake installer content"), 0o644); err != nil {
		t.Fatalf("criar arquivo: %v", err)
	}

	name, err := c.RegisterArtifactIDForFile(filePath, "selfupdate:abc123")
	if err != nil {
		t.Fatalf("RegisterArtifactIDForFile: %v", err)
	}
	if name != "selfupdate-abc123.exe" {
		t.Fatalf("name = %q, want %q", name, "selfupdate-abc123.exe")
	}

	// Sidecar .meta deve existir com o artifactID correto.
	gotID := loadArtifactMeta(dir, "selfupdate-abc123.exe")
	if gotID != "selfupdate:abc123" {
		t.Fatalf("sidecar artifactID = %q, want %q", gotID, "selfupdate:abc123")
	}

	// ListArtifacts deve anunciar o artifactID canônico (não derivado do nome).
	artifacts, err := c.ListArtifacts()
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	found := false
	for _, a := range artifacts {
		if a.ArtifactName == "selfupdate-abc123.exe" {
			found = true
			if a.ArtifactID != "selfupdate:abc123" {
				t.Fatalf("ListArtifacts artifactID = %q, want %q", a.ArtifactID, "selfupdate:abc123")
			}
		}
	}
	if !found {
		t.Fatalf("artifact selfupdate-abc123.exe não listado")
	}
}

func TestRegisterArtifactIDForFile_RejectsPathOutsideTempDir(t *testing.T) {
	c, _ := newRegisterTestCoordinator(t)

	outside := filepath.Join(t.TempDir(), "selfupdate-outside.exe")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("criar arquivo: %v", err)
	}

	if _, err := c.RegisterArtifactIDForFile(outside, "selfupdate:abc"); err == nil {
		t.Fatalf("esperava erro para path fora do P2P_Temp")
	}
}

// TestRegisterArtifactIDForFile_CaseInsensitiveDir valida que a comparação do
// diretório é case-insensitive no Windows (C:\Temp e c:\temp são o mesmo dir).
func TestRegisterArtifactIDForFile_CaseInsensitiveDir(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("comportamento específico do Windows")
	}
	c, dir := newRegisterTestCoordinator(t)

	// Alterna o case da primeira letra do diretório.
	altDir := dir
	if dir[0] >= 'A' && dir[0] <= 'Z' {
		altDir = string(dir[0]+'a'-'A') + dir[1:]
	} else if dir[0] >= 'a' && dir[0] <= 'z' {
		altDir = string(dir[0]+'A'-'a') + dir[1:]
	}

	filePath := filepath.Join(altDir, "selfupdate-case.exe")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatalf("criar arquivo: %v", err)
	}

	if _, err := c.RegisterArtifactIDForFile(filePath, "selfupdate:case"); err != nil {
		t.Fatalf("RegisterArtifactIDForFile com case divergente: %v", err)
	}
}

func TestRegisterArtifactIDForFile_RejectsEmptyOrMissing(t *testing.T) {
	c, dir := newRegisterTestCoordinator(t)

	if _, err := c.RegisterArtifactIDForFile("", "selfupdate:abc"); err == nil {
		t.Fatalf("esperava erro para path vazio")
	}
	if _, err := c.RegisterArtifactIDForFile(filepath.Join(dir, "x.exe"), ""); err == nil {
		t.Fatalf("esperava erro para artifactID vazio")
	}
	if _, err := c.RegisterArtifactIDForFile(filepath.Join(dir, "nao-existe.exe"), "selfupdate:abc"); err == nil {
		t.Fatalf("esperava erro para arquivo inexistente")
	}
}

func TestRegisterArtifactIDForFile_RejectsEmptyFile(t *testing.T) {
	c, dir := newRegisterTestCoordinator(t)

	filePath := filepath.Join(dir, "selfupdate-empty.exe")
	if err := os.WriteFile(filePath, nil, 0o644); err != nil {
		t.Fatalf("criar arquivo: %v", err)
	}
	if _, err := c.RegisterArtifactIDForFile(filePath, "selfupdate:abc"); err == nil {
		t.Fatalf("esperava erro para arquivo vazio")
	}
}

func TestRegisterArtifactIDForFile_OverwritesPreviousID(t *testing.T) {
	c, dir := newRegisterTestCoordinator(t)

	filePath := filepath.Join(dir, "selfupdate-abc123.exe")
	if err := os.WriteFile(filePath, []byte("content"), 0o644); err != nil {
		t.Fatalf("criar arquivo: %v", err)
	}

	if _, err := c.RegisterArtifactIDForFile(filePath, "selfupdate:old"); err != nil {
		t.Fatalf("primeiro registro: %v", err)
	}
	if _, err := c.RegisterArtifactIDForFile(filePath, "selfupdate:new"); err != nil {
		t.Fatalf("segundo registro: %v", err)
	}
	if got := loadArtifactMeta(dir, "selfupdate-abc123.exe"); got != "selfupdate:new" {
		t.Fatalf("sidecar artifactID = %q, want %q", got, "selfupdate:new")
	}
}

func TestAppendAuditSelfupdateSource(t *testing.T) {
	c := &Coordinator{deps: &mockDeps{}}

	// Artifact selfupdate-* deve ter source forçado para "selfupdate".
	c.appendAudit("pull", "selfupdate-abc123.exe", "peer-1", "libp2p", true, "ok")
	events := c.ListAuditEvents()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Source != "selfupdate" {
		t.Fatalf("source = %q, want %q", events[0].Source, "selfupdate")
	}

	// Artifact normal mantém o source original.
	c.appendAudit("pull", "outro-installer.exe", "peer-1", "libp2p", true, "ok")
	events = c.ListAuditEvents()
	if events[0].Source != "libp2p" {
		t.Fatalf("source = %q, want %q", events[0].Source, "libp2p")
	}
	if !strings.EqualFold(events[0].ArtifactName, "outro-installer.exe") {
		t.Fatalf("artifactName = %q", events[0].ArtifactName)
	}
}
