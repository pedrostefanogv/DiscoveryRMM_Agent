package p2p

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// RegisterArtifactIDForFile associa um artifactID canônico a um arquivo que JÁ
// está no diretório P2P_Temp (ex.: "selfupdate-<sha256>.exe" baixado via HTTP
// pelo selfupdate), gravando o sidecar .meta sem recopiar o arquivo.
//
// Sem isso, o artifactID anunciado no gossip seria derivado do nome do arquivo
// ("name:selfupdate-<sha256>.exe"), enquanto o selfupdate procura por
// "selfupdate:<sha256>" — o download P2P nunca seria encontrado.
//
// Validações:
//   - path deve estar dentro de P2PTempDir() (evita associar IDs a arquivos arbitrários)
//   - o arquivo deve existir e ter tamanho > 0
//   - artifactID não pode ser vazio
//
// Retorna o nome do arquivo (ArtifactName) registrado.
func (c *Coordinator) RegisterArtifactIDForFile(path, artifactID string) (string, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return "", fmt.Errorf("artifactID nao informado")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path nao informado")
	}

	dir := c.deps.P2PTempDir()
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("path invalido: %w", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("tempdir invalido: %w", err)
	}
	// O arquivo deve estar exatamente no P2P_Temp (não em subdiretórios).
	// Comparação case-insensitive no Windows (filesystem é case-insensitive;
	// C:\Windows\Temp e c:\windows\temp são o mesmo diretório).
	dirMatches := filepath.Dir(absPath) != absDir
	if runtime.GOOS == "windows" {
		dirMatches = !strings.EqualFold(filepath.Dir(absPath), absDir)
	}
	if dirMatches {
		return "", fmt.Errorf("arquivo fora do P2P_Temp: %s", absPath)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("arquivo inexistente: %w", err)
	}
	if info.IsDir() || info.Size() == 0 {
		return "", fmt.Errorf("arquivo invalido (dir ou vazio): %s", absPath)
	}

	name := SanitizeArtifactName(filepath.Base(absPath))
	if name == "" {
		return "", fmt.Errorf("nome de arquivo invalido: %s", filepath.Base(absPath))
	}

	if err := saveArtifactMeta(dir, name, artifactID); err != nil {
		return "", fmt.Errorf("falha ao salvar sidecar .meta: %w", err)
	}

	c.deps.Log(fmt.Sprintf("[p2p] artifactID registrado para arquivo existente: %s -> %s", name, artifactID))
	return name, nil
}
