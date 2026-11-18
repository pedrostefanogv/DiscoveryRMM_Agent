//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	wellKnownSidUsers          = "*S-1-5-32-545" // Builtin\Users
	trustedInstallerSIDString  = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

// IsElevated retorna true se o processo atual está elevado (TokenElevation
// ativo — Administrador ou SYSTEM). A aplicação da DACL de arquivos de
// segredo NÃO depende mais dela: HardenSecretFileACL é tentada sempre e o
// caller apenas loga a falha (a escrita do serviço continua).
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
// (config.json com deployToken/authTokenDpapi, debug_config.json, chat_config.json,
// discovery.db, libp2p-identity.key).
//
// Remove a herança (PROTECTED_DACL_SECURITY_INFORMATION) e concede controle total
// exclusivamente para NT AUTHORITY\SYSTEM e BUILTIN\Administrators — sem qualquer
// acesso de leitura ou modificação para BUILTIN\Users ou Everyone.
func HardenSecretFileACL(path string) error {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return errors.New("caminho vazio para HardenSecretFileACL")
	}
	if err := verifyNoReparsePoint(cleanPath); err != nil {
		return fmt.Errorf("arquivo de segredo invalido: %w", err)
	}

	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("SID SYSTEM: %w", err)
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("SID Administrators: %w", err)
	}

	var pinner runtime.Pinner
	pinner.Pin(systemSID)
	pinner.Pin(adminSID)
	defer pinner.Unpin()

	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF

	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(systemSID),
			},
		},
		{
			AccessPermissions: fileAllAccess,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_ALIAS,
				TrusteeValue: windows.TrusteeValueFromSID(adminSID),
			},
		},
	}

	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("ACLFromEntries falhou: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		cleanPath,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("SetNamedSecurityInfo falhou: %w", err)
	}
	return nil
}

const fileDeleteChild uint32 = 0x00000040

const untrustedWriteMask uint32 = windows.FILE_WRITE_DATA |
	windows.FILE_APPEND_DATA |
	windows.FILE_WRITE_EA |
	windows.FILE_WRITE_ATTRIBUTES |
	fileDeleteChild |
	windows.DELETE |
	windows.WRITE_DAC |
	windows.WRITE_OWNER |
	windows.GENERIC_WRITE |
	windows.GENERIC_ALL

// VerifyTrustedSecretFileACL valida de forma estrita um arquivo de segredo
// (ex.: debug_config.json no watcher):
//   - não é symlink nem reparse point (nem o diretório pai);
//   - o dono (owner) é SYSTEM ou BUILTIN\Administrators;
//   - a DACL é não-nula e toda ACE ALLOW efetiva pertence exclusivamente a
//     SYSTEM ou BUILTIN\Administrators (nenhum acesso a Users/Everyone);
//   - o diretório pai também pertence a SYSTEM/Administrators e não concede
//     escrita/exclusão/take-ownership a contas não administrativas.
func VerifyTrustedSecretFileACL(path string) error {
	return verifySecretFileACL(path, true)
}

// VerifyTrustedSecretFileWritableSafety valida se o arquivo (e o diretório pai)
// pertence a SYSTEM/Administrators e não é gravável por usuários comuns,
// permitindo migração segura de arquivos pré-hardening com Users:R legado.
func VerifyTrustedSecretFileWritableSafety(path string) error {
	return verifySecretFileACL(path, false)
}

func verifySecretFileACL(path string, strictNoExtraRead bool) error {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return errors.New("caminho vazio")
	}
	if err := verifyNoReparsePoint(cleanPath); err != nil {
		return err
	}
	parentDir := filepath.Dir(cleanPath)
	if err := verifyNoReparsePoint(parentDir); err != nil {
		return fmt.Errorf("diretorio pai inseguro (%s): %w", parentDir, err)
	}
	if err := verifyTrustedDirectoryWriteACL(parentDir, false); err != nil {
		return fmt.Errorf("diretorio pai nao confiavel (%s): %w", parentDir, err)
	}

	sd, err := windows.GetNamedSecurityInfo(cleanPath, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("GetNamedSecurityInfo(%s): %w", cleanPath, err)
	}
	if sd == nil {
		return errors.New("security descriptor nulo")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("consultar owner(%s): %w", cleanPath, err)
	}
	if !isAdminOrSystemSID(owner, false) {
		return fmt.Errorf("owner nao confiavel (%s)", sidToStringSafe(owner))
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("consultar DACL(%s): %w", cleanPath, err)
	}
	return inspectFileDACL(dacl, strictNoExtraRead, false)
}

// VerifyTrustedExecutableACL valida que um binário executável (ex.: uninstall.exe)
// e seu diretório pai não são reparse points, têm dono administrativo/TrustedInstaller
// e não possuem permissões de escrita/modificação para não-administradores.
func VerifyTrustedExecutableACL(path string) error {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return errors.New("caminho vazio")
	}
	if err := verifyNoReparsePoint(cleanPath); err != nil {
		return err
	}
	parentDir := filepath.Dir(cleanPath)
	if err := verifyNoReparsePoint(parentDir); err != nil {
		return fmt.Errorf("diretorio pai inseguro (%s): %w", parentDir, err)
	}
	if err := verifyTrustedDirectoryWriteACL(parentDir, true); err != nil {
		return fmt.Errorf("diretorio do executavel nao confiavel (%s): %w", parentDir, err)
	}

	sd, err := windows.GetNamedSecurityInfo(cleanPath, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("GetNamedSecurityInfo(%s): %w", cleanPath, err)
	}
	if sd == nil {
		return errors.New("security descriptor nulo")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("consultar owner(%s): %w", cleanPath, err)
	}
	if !isAdminOrSystemSID(owner, true) {
		return fmt.Errorf("owner do executavel nao confiavel (%s)", sidToStringSafe(owner))
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("consultar DACL(%s): %w", cleanPath, err)
	}
	return inspectFileDACL(dacl, false, true)
}

func verifyTrustedDirectoryWriteACL(dir string, allowTrustedInstaller bool) error {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("GetNamedSecurityInfo(%s): %w", dir, err)
	}
	if sd == nil {
		return errors.New("security descriptor do diretorio nulo")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("consultar owner(%s): %w", dir, err)
	}
	if !isAdminOrSystemSID(owner, allowTrustedInstaller) {
		return fmt.Errorf("owner do diretorio nao confiavel (%s)", sidToStringSafe(owner))
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("consultar DACL(%s): %w", dir, err)
	}
	return inspectFileDACL(dacl, false, allowTrustedInstaller)
}

func inspectFileDACL(dacl *windows.ACL, strictNoExtraRead bool, allowTrustedInstaller bool) error {
	if dacl == nil {
		return errors.New("DACL nula (acesso irrestrito)")
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("GetAce(%d): %w", i, err)
		}
		if ace == nil {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if isAdminOrSystemSID(trustee, allowTrustedInstaller) {
			continue
		}
		if strictNoExtraRead {
			return fmt.Errorf("ACE nao permitida para trustee %s (mask=0x%x)", sidToStringSafe(trustee), uint32(ace.Mask))
		}
		if uint32(ace.Mask)&untrustedWriteMask != 0 {
			return fmt.Errorf("ACE gravavel para trustee nao administrativo %s (mask=0x%x)", sidToStringSafe(trustee), uint32(ace.Mask))
		}
	}
	return nil
}

func verifyNoReparsePoint(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("reparse/symlink proibido: %s", path)
	}
	p16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p16)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("reparse point proibido: %s", path)
	}
	return nil
}

func isAdminOrSystemSID(sid *windows.SID, allowTrustedInstaller bool) bool {
	if sid == nil {
		return false
	}
	if systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid); err == nil && sid.Equals(systemSID) {
		return true
	}
	if adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid); err == nil && sid.Equals(adminSID) {
		return true
	}
	if allowTrustedInstaller {
		if tiSID, err := windows.StringToSid(trustedInstallerSIDString); err == nil && sid.Equals(tiSID) {
			return true
		}
	}
	return false
}

func sidToStringSafe(sid *windows.SID) string {
	if sid == nil {
		return "<nil>"
	}
	return sid.String()
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
