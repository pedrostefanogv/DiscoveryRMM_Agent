package app

import "testing"

// YAML REAL do `winget download --id Brave.Brave` (trecho relevante): é este
// manifesto que descreve o binário que o agente efetivamente baixa.
const braveManifestYAML = `PackageIdentifier: Brave.Brave
PackageVersion: 155.1.97.56
PackageLocale: en-US
ShortDescription: Secure, Fast & Private Web Browser with Adblocker
Installers:
- Architecture: x64
  InstallerType: exe
  InstallerSha256: 860fcd9b0daaa7d2647d5230b16aa7b7f1be0ca03db49944a925e24a2600a9a2
  InstallerUrl: https://github.com/brave/brave-browser/releases/download/v1.97.56/BraveBrowserStandaloneSilentSetup.exe
  Scope: user
  ReleaseDate: 2026-10-07
  InstallModes:
  - silent
ManifestVersion: 1.12.0
`

const machineManifestYAML = `PackageIdentifier: Vendor.App
PackageVersion: 1.0.0
Installers:
- Architecture: x64
  InstallerType: nullsoft
  Scope: machine
  InstallerSwitches:
    Silent: /S
    SilentWithProgress: /S
  InstallerUrl: https://example.invalid/app.exe
`

// O caso Brave: instalador `Scope: user` e SEM switches declarados. Dirigir esse
// binário como SYSTEM com os switches do catálogo (`/silent /install`) faz o stub
// subir o updater e travar, deixando processos órfãos — nada é instalado.
func TestParseWingetManifestInfo_BraveIsUserScopeWithoutSwitches(t *testing.T) {
	info := parseWingetManifestInfo(braveManifestYAML)
	if info.Scope != "user" {
		t.Fatalf("scope = %q, want user", info.Scope)
	}
	if info.InstallerType != "exe" {
		t.Fatalf("installerType = %q, want exe", info.InstallerType)
	}
	if info.Silent != "" || info.SilentWithProgress != "" {
		t.Fatalf("manifesto do Brave NAO declara switches, obtido silent=%q silentWithProgress=%q", info.Silent, info.SilentWithProgress)
	}
}

// Manifesto machine-scope com switches declarados: aqui a execução direta é
// legítima e deve usar EXATAMENTE esses switches.
func TestParseWingetManifestInfo_MachineScopeWithSwitches(t *testing.T) {
	info := parseWingetManifestInfo(machineManifestYAML)
	if info.Scope != "machine" {
		t.Fatalf("scope = %q, want machine", info.Scope)
	}
	if info.InstallerType != "nullsoft" {
		t.Fatalf("installerType = %q, want nullsoft", info.InstallerType)
	}
	if info.Silent != "/S" || info.SilentWithProgress != "/S" {
		t.Fatalf("switches do manifesto nao lidos: %q / %q", info.Silent, info.SilentWithProgress)
	}
}

// YAML vazio/lixo não pode entrar em pânico nem inventar dados.
func TestParseWingetManifestInfo_DegenerateInputs(t *testing.T) {
	for _, in := range []string{"", "   ", "Installers:", "Installers:\n- Architecture: x64\n", "not: [valid: yaml"} {
		got := parseWingetManifestInfo(in)
		if got.Scope != "" || got.Silent != "" {
			t.Fatalf("entrada %q deveria resultar em campos vazios, obtido %+v", in, got)
		}
	}
}
