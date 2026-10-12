package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guarda de regressao do caminho de download dos anexos do chamado.
//
// BUG CORRIGIDO: o anexo era salvo em
// C:\WINDOWS\system32\config\systemprofile\Downloads porque o servico (SYSTEM)
// resolvia a home do proprio perfil. Alem disso nao havia como o usuario
// escolher onde salvar.
func TestSanitizeAttachmentFileName(t *testing.T) {
	traversal := filepath.Join("..", "..", "etc", "passwd")
	cases := map[string]string{
		// No Windows "b:" e lido como volume e filepath.Base descarta o prefixo.
		"b:c*d?e.png": "c-d-e.png",
		traversal:     "passwd",
		"":            "anexo",
		"   ":         "anexo",
	}
	for input, want := range cases {
		if got := sanitizeAttachmentFileName(input); got != want {
			t.Fatalf("sanitizeAttachmentFileName(%q) = %q, esperado %q", input, got, want)
		}
	}
}

func TestSaveDownloadedAttachmentToDirNaoSobrescreve(t *testing.T) {
	dir := t.TempDir()

	first, err := saveDownloadedAttachmentToDir(dir, "nota.txt", []byte("primeiro"))
	if err != nil {
		t.Fatalf("primeira gravacao: %v", err)
	}
	if first != filepath.Join(dir, "nota.txt") {
		t.Fatalf("caminho inesperado: %q", first)
	}

	second, err := saveDownloadedAttachmentToDir(dir, "nota.txt", []byte("segundo"))
	if err != nil {
		t.Fatalf("segunda gravacao: %v", err)
	}
	if second == first {
		t.Fatal("segunda gravacao deveria receber timestamp em vez de sobrescrever")
	}
	if !strings.HasPrefix(filepath.Base(second), "nota-") {
		t.Fatalf("nome com timestamp inesperado: %q", filepath.Base(second))
	}

	original, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("ler original: %v", err)
	}
	if string(original) != "primeiro" {
		t.Fatalf("conteudo original alterado: %q", string(original))
	}
}

func TestSaveAttachmentBytesAtCaminhoEscolhido(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "escolhido.png")

	got, err := saveAttachmentBytesAt(target, "ignorado.png", []byte("conteudo"))
	if err != nil {
		t.Fatalf("gravacao no caminho escolhido: %v", err)
	}
	if got != target {
		t.Fatalf("deveria gravar exatamente em %q, gravou em %q", target, got)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ler destino: %v", err)
	}
	if string(data) != "conteudo" {
		t.Fatalf("conteudo gravado difere: %q", string(data))
	}

	if _, err := saveAttachmentBytesAt("relativo.png", "x.png", []byte("x")); err == nil {
		t.Fatal("caminho relativo deveria ser recusado")
	}
	if _, err := saveAttachmentBytesAt(target, "x.png", nil); err == nil {
		t.Fatal("anexo vazio deveria ser recusado")
	}

	// destino que e PASTA cai no fluxo por pasta (nao tenta gravar um arquivo
	// com o nome da pasta).
	asDir, err := saveAttachmentBytesAt(dir, "dentro.txt", []byte("x"))
	if err != nil {
		t.Fatalf("destino pasta: %v", err)
	}
	if asDir != filepath.Join(dir, "dentro.txt") {
		t.Fatalf("destino pasta gerou %q", asDir)
	}
}

// O override por ambiente tem prioridade — permite fixar a pasta sem depender
// do token da sessao interativa.
func TestResolveAttachmentDownloadDirOverride(t *testing.T) {
	custom := t.TempDir()
	t.Setenv(attachmentDownloadDirOverrideEnv, custom)
	if got := resolveAttachmentDownloadDir(); got != custom {
		t.Fatalf("resolveAttachmentDownloadDir() = %q, esperado %q", got, custom)
	}
}

// Em modo companion o resultado do RPC é reidratado com json.Unmarshal: se o
// campo do caminho não for exportado, a UI recebe vazio (o arquivo é salvo mas o
// usuário não vê onde).
func TestAttachmentSaveResultSobreviveAoRPC(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"path": filepath.Join("C:", "Users", "ana", "Downloads", "nota.txt")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var res attachmentSaveResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.TrimSpace(res.Path) == "" {
		t.Fatal("attachmentSaveResult.Path precisa ser exportado para sobreviver ao RPC")
	}
}

// Perfil do sistema (servico) NAO pode ser usado como pasta de download.
func TestResolveAttachmentDownloadDirNaoUsaPerfilDoSistema(t *testing.T) {
	systemProfile := filepath.Join(t.TempDir(), "Windows", "system32", "config", "systemprofile")
	t.Setenv(attachmentDownloadDirOverrideEnv, "")
	t.Setenv("USERPROFILE", systemProfile)
	t.Setenv("HOME", systemProfile)

	dir := resolveAttachmentDownloadDir()
	if dir == "" {
		t.Fatal("resolveAttachmentDownloadDir nao deveria devolver vazio")
	}
	if isSystemProfilePath(dir) {
		t.Fatalf("pasta de download caiu no perfil do sistema: %q", dir)
	}
}
