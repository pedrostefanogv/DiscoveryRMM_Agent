package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// wingetManifestInfo é o que o MANIFESTO do winget diz sobre o instalador que o
// próprio winget baixou (`winget download` grava o YAML ao lado do arquivo).
//
// Por que isso importa: o agente baixa o instalador pelo manifesto e depois o
// executava com os switches do CATÁLOGO da loja, que descrevem outro binário.
// Caso Brave: o catálogo aponta `BraveBrowserStandaloneSetup.exe` com
// `/silent /install`, mas o download traz `BraveBrowserStandaloneSilentSetup.exe`
// (Scope: user, SEM InstallerSwitches). Dirigir o SilentSetup com switches
// inventados o faz subir o updater (BraveUpdate) e TRAVAR como SYSTEM, deixando
// processos órfãos — e nada é instalado.
type wingetManifestInfo struct {
	Scope              string `json:"scope,omitempty"`              // user | machine
	Silent             string `json:"silent,omitempty"`             // InstallerSwitches.Silent
	SilentWithProgress string `json:"silentWithProgress,omitempty"` // InstallerSwitches.SilentWithProgress
	InstallerType      string `json:"installerType,omitempty"`
}

// parseWingetManifestInfo extrai os campos do YAML do manifesto (subconjunto
// plano emitido pelo `winget download`: PackageIdentifier/Installers/...).
// Puro e testável; campos ausentes ficam vazios.
func parseWingetManifestInfo(yamlText string) wingetManifestInfo {
	var info wingetManifestInfo
	section := ""
	for _, raw := range strings.Split(yamlText, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		// Seções de primeiro nível encerram a anterior. Itens de lista
		// ("- Architecture: x64") NÃO são seção: pertencem a "Installers".
		if indent == 0 && !strings.HasPrefix(trimmed, "-") {
			section = strings.TrimSuffix(trimmed, ":")
			continue
		}
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		switch section {
		case "Installers":
			// Vale o primeiro instalador (todos os arquitetos do mesmo pacote
			// compartilham o mesmo tipo/escopo em Brave, Chrome etc.).
			switch key {
			case "Scope":
				if info.Scope == "" {
					info.Scope = strings.ToLower(value)
				}
			case "InstallerType":
				if info.InstallerType == "" {
					info.InstallerType = strings.ToLower(value)
				}
			case "Silent":
				if info.Silent == "" {
					info.Silent = value
				}
			case "SilentWithProgress":
				if info.SilentWithProgress == "" {
					info.SilentWithProgress = value
				}
			}
		}
	}
	return info
}

// manifestInfoFromDownloadDir procura o YAML gerado pelo `winget download` no
// diretório e devolve o que ele diz sobre o instalador.
func manifestInfoFromDownloadDir(dir string) (wingetManifestInfo, bool) {
	var yamlPath string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || yamlPath != "" {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".yaml" || ext == ".yml" {
			yamlPath = path
			return filepath.SkipAll
		}
		return nil
	})
	if yamlPath == "" {
		return wingetManifestInfo{}, false
	}
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return wingetManifestInfo{}, false
	}
	return parseWingetManifestInfo(string(data)), true
}

// artifactManifestPath é o sidecar que guarda o que o manifesto diz sobre o
// instalador em cache (o arquivo é renomeado no P2P, então o YAML original não
// acompanha o artifact).
func (m *automationPackageManagerRouter) artifactManifestPath(artifactName string) string {
	return filepath.Join(m.app.p2pTempDir(), artifactName+".manifest.json")
}

// writeArtifactManifest persiste o sidecar (best-effort).
func (m *automationPackageManagerRouter) writeArtifactManifest(artifactName string, info wingetManifestInfo) {
	if m == nil || m.app == nil || strings.TrimSpace(artifactName) == "" {
		return
	}
	data, err := json.Marshal(info)
	if err != nil {
		return
	}
	if err := os.WriteFile(m.artifactManifestPath(artifactName), data, 0o644); err != nil {
		m.logf("[automation][p2p] aviso: falha ao gravar manifesto do artifact %s: %v", artifactName, err)
	}
}

// artifactManifestInfo lê o sidecar. found=false significa "não sabemos o que o
// manifesto diz" — nesse caso o router NÃO dirige o instalador com switches do
// catálogo (ver installViaP2P).
func (m *automationPackageManagerRouter) artifactManifestInfo(artifactName string) (wingetManifestInfo, bool) {
	if m == nil || m.app == nil || strings.TrimSpace(artifactName) == "" {
		return wingetManifestInfo{}, false
	}
	data, err := os.ReadFile(m.artifactManifestPath(artifactName))
	if err != nil {
		return wingetManifestInfo{}, false
	}
	var info wingetManifestInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return wingetManifestInfo{}, false
	}
	return info, true
}
