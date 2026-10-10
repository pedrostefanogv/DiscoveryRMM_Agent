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
	// ArtifactSizeBytes vincula o sidecar AO ARQUIVO: se o artifact for
	// substituído (nova versão/variante) o tamanho muda e o sidecar é tratado
	// como desconhecido — sem isso, metadata antiga poderia autorizar a
	// execução direta do binário errado, com switches que não são dele.
	ArtifactSizeBytes int64 `json:"artifactSizeBytes,omitempty"`
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

// installerBlocks separa as entradas de `Installers:` do YAML do manifesto.
// O manifesto merged pode ter VÁRIAS (ex.: Brave.Brave com variante user e
// machine), e elas podem descrever binários/switches diferentes.
func installerBlocks(yamlText string) []string {
	var blocks []string
	inInstallers := false
	var current []string
	flush := func() {
		if len(current) > 0 {
			blocks = append(blocks, strings.Join(current, "\n"))
			current = nil
		}
	}
	for _, raw := range strings.Split(yamlText, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent == 0 && !strings.HasPrefix(trimmed, "-") {
			if inInstallers {
				break // fim da seção Installers
			}
			inInstallers = strings.TrimSuffix(trimmed, ":") == "Installers"
			continue
		}
		if !inInstallers {
			continue
		}
		if indent == 0 && strings.HasPrefix(trimmed, "-") {
			flush()
			current = []string{trimmed}
			continue
		}
		if len(current) > 0 {
			current = append(current, trimmed)
		}
	}
	flush()
	return blocks
}

// parseInstallerBlock lê os campos relevantes de UMA entrada de Installers.
func parseInstallerBlock(block string) wingetManifestInfo {
	var info wingetManifestInfo
	for _, raw := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "-"))
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "\"'")
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
	return info
}

// parseWingetManifestInfoAgreed devolve os dados do instalador SOMENTE quando
// TODAS as entradas do manifesto concordam em escopo/tipo/switches. Se
// divergirem (ex.: Brave com variante user e machine), o resultado é
// desconhecido — e desconhecido significa "não dirija o binário", que é o lado
// seguro: dirigir a variante errada com switches de outra entrada é exatamente
// o que travava o stub do Brave.
func parseWingetManifestInfoAgreed(yamlText string) (wingetManifestInfo, bool) {
	blocks := installerBlocks(yamlText)
	if len(blocks) == 0 {
		return wingetManifestInfo{}, false
	}
	first := parseInstallerBlock(blocks[0])
	for _, block := range blocks[1:] {
		if parseInstallerBlock(block) != first {
			return wingetManifestInfo{}, false
		}
	}
	return first, true
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
	return parseWingetManifestInfoAgreed(string(data))
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
	// Sem o tamanho do artifact o sidecar NÃO é gravado: sem esse vínculo a
	// auto-invalidação na leitura não teria como detectar que o arquivo mudou,
	// e metadata antiga poderia autorizar execução direta do binário errado.
	stat, statErr := os.Stat(filepath.Join(m.app.p2pTempDir(), artifactName))
	if statErr != nil || stat.IsDir() || stat.Size() <= 0 {
		m.logf("[automation][p2p] aviso: artifact %s indisponivel para vincular o manifesto (%v) - sidecar nao gravado", artifactName, statErr)
		return
	}
	info.ArtifactSizeBytes = stat.Size()
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
	// Auto-invalidação ESTRITA: o sidecar só vale para o ARQUIVO exato com que foi
	// gravado. Tamanho ausente/zero (sidecar legado ou artifact ausente) é tratado
	// como desconhecido — nunca como "pode dirigir".
	stat, statErr := os.Stat(filepath.Join(m.app.p2pTempDir(), artifactName))
	if statErr != nil || stat.IsDir() || stat.Size() <= 0 || info.ArtifactSizeBytes <= 0 || stat.Size() != info.ArtifactSizeBytes {
		return wingetManifestInfo{}, false
	}
	return info, true
}
