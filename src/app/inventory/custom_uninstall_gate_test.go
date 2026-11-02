package inventory

import "testing"

// Trava a otimização da desinstalação de apps custom: o instalador publicado só
// é baixado/extraído quando o comando usa {{installer}}/{{installerDir}}. Um
// comando que aponta direto para o desinstalador já instalado (ex.: Program
// Files) NÃO pode disparar download de GBs.
func TestUninstallGateSkipsDownloadWithoutPlaceholders(t *testing.T) {
	commands := []struct {
		command     string
		needsFile   bool
		description string
	}{
		{`"C:\Program Files\Foxit Software\Foxit PDF Editor\uninstall.exe" /quiet /clean`, false, "caminho absoluto do desinstalador instalado"},
		{`MsiExec.exe /X{12345678-1234-1234-1234-123456789012} /qn`, false, "msiexec por ProductCode"},
		{`powershell -NoProfile -Command "Get-Package 'Foxit*' | Uninstall-Package -Force"`, false, "desinstalação via PowerShell sem placeholders"},
		{`{{installerDir}} /uninstall /quiet /clean`, true, "placeholder do diretório extraído"},
		{`{{installer}} /S /uninstall`, true, "placeholder do arquivo do instalador"},
		{`%INSTALLER_DIR%\uninstall.exe /quiet`, true, "placeholder legado do diretório"},
	}

	for _, tc := range commands {
		if got := commandReferencesInstaller(tc.command); got != tc.needsFile {
			t.Fatalf("%s (%q): commandReferencesInstaller=%v, esperado %v", tc.description, tc.command, got, tc.needsFile)
		}
	}
}

// Placeholder ausente não pode ser "substituído" por vazio: comando com caminho
// absoluto volta intacto, garantindo que o shell roda o caminho informado.
func TestApplyCustomCommandPlaceholdersKeepsAbsoluteCommands(t *testing.T) {
	const command = `"C:\Program Files\App\uninstall.exe" /quiet`
	if got := applyCustomCommandPlaceholders(command, "", ""); got != command {
		t.Fatalf("comando absoluto foi alterado: %q", got)
	}
}
