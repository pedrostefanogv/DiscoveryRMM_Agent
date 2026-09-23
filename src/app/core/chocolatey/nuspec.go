package chocolatey

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
)

// InstalledPackageInfo descreve um pacote instalado localmente no Chocolatey.
type InstalledPackageInfo struct {
	ID      string
	Title   string
	Version string
}

// nuspecDocument é o subconjunto do .nuspec que interessa para a correlação.
type nuspecDocument struct {
	Metadata struct {
		ID      string `xml:"id"`
		Title   string `xml:"title"`
		Version string `xml:"version"`
	} `xml:"metadata"`
}

// chocolateyInstallDir resolve o diretório de instalação do Chocolatey.
func chocolateyInstallDir() string {
	if dir := strings.TrimSpace(os.Getenv("ChocolateyInstall")); dir != "" {
		return dir
	}
	return `C:\ProgramData\chocolatey`
}

// ScanInstalledPackages lê os .nuspec em "<base>lib<id><id>.nuspec" para
// obter o título de exibição (o "choco list" só devolve id|version). É o elo
// local entre o nome do registro e o Id do pacote choco, sem depender do
// catálogo da loja. Nunca falha: retorna apenas o que conseguir ler.
func ScanInstalledPackages() []InstalledPackageInfo {
	libDir := filepath.Join(chocolateyInstallDir(), "lib")
	entries, err := os.ReadDir(libDir)
	if err != nil {
		return nil
	}

	result := make([]InstalledPackageInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		nuspecPath := findNuspec(filepath.Join(libDir, entry.Name()))
		if nuspecPath == "" {
			continue
		}
		info, err := readNuspec(nuspecPath)
		if err != nil || strings.TrimSpace(info.ID) == "" {
			continue
		}
		result = append(result, info)
	}
	return result
}

func findNuspec(packageDir string) string {
	entries, err := os.ReadDir(packageDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".nuspec") {
			return filepath.Join(packageDir, entry.Name())
		}
	}
	return ""
}

func readNuspec(path string) (InstalledPackageInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return InstalledPackageInfo{}, err
	}

	var doc nuspecDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return InstalledPackageInfo{}, err
	}

	return InstalledPackageInfo{
		ID:      strings.TrimSpace(doc.Metadata.ID),
		Title:   strings.TrimSpace(doc.Metadata.Title),
		Version: strings.TrimSpace(doc.Metadata.Version),
	}, nil
}
