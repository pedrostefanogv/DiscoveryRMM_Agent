//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"discovery/app/core/processutil"
)

// SIDs bem-conhecidos usados nos grants (independentes do idioma do Windows).
const (
	wellKnownSidEveryone       = "*S-1-1-0"      // Everyone
	wellKnownSidSystem         = "*S-1-5-18"     // SYSTEM
	wellKnownSidAdministrators = "*S-1-5-32-544" // Builtin\Administrators
)
// IsElevated retorna true se o processo atual está elevado (TokenElevation
// ativo — Administrador ou SYSTEM). Usado para decidir se é seguro aplicar
// DACLs restritas em arquivos de segredo: em contexto elevado a aplicação
// funciona (SYSTEM/Admins mantêm acesso); em modo standalone não elevado
// a aplicação da DACL falharia e quebraria a escrita da própria UI.
func IsElevated() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()

	var elevation uint32
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevation)), uint32(unsafe.Sizeof(elevation)), &size); err != nil {
		return false
	}
	return elevation != 0
}

// EnsureSharedStagingAccess prepara um diretório de staging de artefatos/
// instaladores (ex.: %WINDIR%\Temp\Discovery e ...\Discovery\P2P_Temp) com o
// modelo de compartilhamento seguro do agente:
//
//   - SYSTEM e Administrators: Full Control com herança — quem escreve, limpa
//     e publica artifacts é o serviço (SYSTEM) ou uma UI elevada;
//   - Everyone: Read + Execute com herança — usuários comuns podem ler e
//     lançar instaladores (ShellExecuteEx runas), mas NÃO podem criar nem
//     substituir arquivos.
//
// Correção C7 (relatório de análise): o grant anterior dava Full Control para
// Everyone sobre um diretório de onde o agente elevado executa instaladores —
// qualquer usuário local podia trocar o .exe/.msi entre a verificação de
// checksum e a execução (TOCTOU → LPE para SYSTEM). Com Everyone:(RX) a janela
// de escrita por usuários comuns é fechada sem quebrar o modelo
// "serviço escreve, usuário lê/lança".
//
// SIDs bem-conhecidos são usados no lugar de nomes (Everyone/SYSTEM) para
// funcionar em Windows de qualquer idioma. O grant do Everyone usa o
// modificador replace (/grant:r), que substitui os grants explícitos legados
// desse trustee (ex.: Everyone:(F) de instalações anteriores) por RX.
//
// Se o diretório não existir, é criado primeiro (MkdirAll).
func EnsureSharedStagingAccess(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("criar diretorio: %w", err)
	}

	// A ACL é aplicada apenas na raiz; a herança (OI)(CI) replica nos filhos.
	cmd := exec.Command("icacls.exe", path,
		"/grant:r", wellKnownSidEveryone+":(OI)(CI)RX",
		"/grant", wellKnownSidSystem+":(OI)(CI)F",
		"/grant", wellKnownSidAdministrators+":(OI)(CI)F",
		"/Q")
	processutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls falhou: %w — saida: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HardenSecretFileACL restringe a DACL de um arquivo que contém segredos
// (config.json com deployToken/AuthToken, debug_config.json, chat_config.json).
//
// Correção A3: os.WriteFile(0o600) é no-op no Windows — o arquivo herda a ACL
// do diretório (%ProgramData%\Discovery tem Users:(M)), então qualquer usuário
// local podia LER o token e REESCREVER a configuração (config poisoning).
// Aplica DACL explícita: SYSTEM e Administrators com Full Control; Everyone
// apenas Read+Execute (a UI não elevada continua podendo ler; perde a escrita).
//
// Deve ser chamado APENAS por processos elevados (IsElevated) — a aplicação
// da DACL exige privilégio e, em contexto não elevado (standalone), a escrita
// da própria UI precisa continuar funcionando. Best-effort: o erro é
// retornado para o caller logar, sem bloquear o fluxo de configuração.
func HardenSecretFileACL(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	cmd := exec.Command("icacls.exe", path,
		"/inheritance:r",
		"/grant:r", wellKnownSidSystem+":F",
		"/grant:r", wellKnownSidAdministrators+":F",
		"/grant:r", wellKnownSidEveryone+":RX",
		"/Q")
	processutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls harden falhou: %w — saida: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureWorldReadable grants Everyone read (R) on a single file.
// Used for downloaded artifacts so any user on the machine can read them.
// Usa o SID bem-conhecido do Everyone (*S-1-1-0) para funcionar em qualquer
// idioma de Windows.
func EnsureWorldReadable(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}
	cmd := exec.Command("icacls.exe", filePath, "/grant", wellKnownSidEveryone+":R", "/Q")
	processutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls arquivo falhou: %w — saida: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
