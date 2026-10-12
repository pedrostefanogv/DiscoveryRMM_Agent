package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// distribuição P2P dos instaladores de apps CUSTOM.
//
// O caminho winget/chocolatey já publica o instalador no cache P2P
// (PreloadPackageForP2P / downloadAndCacheForP2P), mas o app custom baixava o
// arquivo da API apenas para instalar localmente e nunca o publicava — cada
// agent do site repetia o download de GBs. Aqui o instalador baixado é
// publicado (vira seed) e, antes de baixar da API, procuramos a MESMA versão
// publicada por um peer.
//
// Convenção de ID: "custom:<packageId normalizado>" — segue o padrão dos
// demais prefixos ("winget:", "selfupdate:") para que o sidecar .meta e o
// gossip carreguem o ID canônico.

// customP2PFetchTimeout limita a tentativa de obter o instalador da rede antes
// de cair para a API (o download em si pode ter GBs; o timeout cobre a
// negociação/início da transferência).
const customP2PFetchTimeout = 10 * time.Minute

// customArtifactID devolve o ArtifactID canônico do instalador de um app custom.
// A versão faz parte do ID ("custom:foxitedito:v2"): assim um artifact de versão
// antiga nunca é servido/reaproveitado depois de um bump no catálogo, mesmo que
// o gossip não carregue o campo Version (o sidecar .meta só guarda o ID).
func customArtifactID(packageID, version string) string {
	key := normalizePackageLookupKey(packageID)
	if key == "" {
		return ""
	}
	if v := sanitizeArtifactIDPart(version); v != "" {
		return "custom:" + key + ":v" + v
	}
	return "custom:" + key
}

// sanitizeArtifactIDPart mantém apenas caracteres seguros para compor o ID
// (letras, dígitos, ponto, hífen e underline), preservando "14.0.8.33807".
func sanitizeArtifactIDPart(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	out := b.String()
	// "v2" e "2" precisam gerar o mesmo ID (o "v" já é o prefixo do sufixo).
	if len(out) > 1 && out[0] == 'v' && out[1] >= '0' && out[1] <= '9' {
		out = out[1:]
	}
	return out
}

// publishCustomInstallerForP2P publica no cache P2P (P2P_Temp) o instalador de
// app custom recém-baixado da API, tornando este agent seed para o site, e
// devolve o caminho do artifact publicado (a instalação passa a rodar a partir
// dele). Best-effort: devolve erro apenas para log; a instalação local não
// depende disso.
func (a *App) publishCustomInstallerForP2P(ctx context.Context, packageID, version, installerPath string) (string, error) {
	if a == nil || a.P2PCoord == nil {
		return "", fmt.Errorf("P2P indisponível neste agent")
	}
	artifactID := customArtifactID(packageID, version)
	if artifactID == "" {
		return "", fmt.Errorf("packageId inválido para artifact P2P: %q", packageID)
	}
	installerPath = strings.TrimSpace(installerPath)
	if installerPath == "" {
		return "", fmt.Errorf("caminho do instalador vazio")
	}
	if _, err := os.Stat(installerPath); err != nil {
		return "", fmt.Errorf("instalador não encontrado para publicar: %w", err)
	}

	published, err := a.P2PCoord.PublishFileWithIDAndVersion(installerPath, artifactID, strings.TrimSpace(version))
	if err != nil {
		return "", fmt.Errorf("publicar %s no cache P2P: %w", artifactID, err)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	a.P2PCoord.RefreshPeerArtifactIndex(ctx, "custom-publish")
	a.Logs.Append(fmt.Sprintf(
		"[p2p][custom] instalador publicado no cache P2P packageId=%s artifactID=%s artifactName=%s versao=%s",
		packageID, published.ArtifactID, published.ArtifactName, strings.TrimSpace(version)))

	return a.localP2PArtifactPathByName(published.ArtifactName), nil
}

// fetchCustomInstallerFromP2P procura o instalador do app custom já publicado
// por algum peer do site e devolve o caminho local do arquivo. Só aceita
// artifact da MESMA versão do catálogo (sem versão comparável, aceita): usar um
// payload antigo instalaria a versão errada. Devolve ok=false quando não há
// fonte utilizável — o chamador então baixa da API.
func (a *App) fetchCustomInstallerFromP2P(ctx context.Context, packageID, version string) (string, bool) {
	if a == nil || a.P2PCoord == nil {
		return "", false
	}
	artifactID := customArtifactID(packageID, version)
	if artifactID == "" {
		return "", false
	}

	// Já temos o arquivo no cache local (fomos seed ou já baixamos antes).
	if path := a.localP2PArtifactPathByID(artifactID, version); path != "" {
		a.Logs.Append(fmt.Sprintf("[p2p][custom] instalador já disponível no cache local packageId=%s", packageID))
		return path, true
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, customP2PFetchTimeout)
	defer cancel()

	a.P2PCoord.RefreshPeerArtifactIndex(ctx, "custom-install")

	artifactName := ""
	for _, peer := range a.GetP2PPeerArtifactIndexCached() {
		for _, artifact := range peer.Artifacts {
			if !strings.EqualFold(strings.TrimSpace(artifact.ArtifactID), artifactID) {
				continue
			}
			if !customArtifactVersionMatches(artifact.Version, version) {
				continue
			}
			artifactName = strings.TrimSpace(artifact.ArtifactName)
			break
		}
		if artifactName != "" {
			break
		}
	}
	if artifactName == "" {
		return "", false
	}

	if _, err := a.P2PCoord.DownloadArtifactSwarm(ctx, artifactName); err != nil {
		a.Logs.Append(fmt.Sprintf("[p2p][custom] download via swarm falhou packageId=%s artifact=%s: %v",
			packageID, artifactName, err))
		return "", false
	}
	if path := a.localP2PArtifactPathByName(artifactName); path != "" {
		a.Logs.Append(fmt.Sprintf("[p2p][custom] instalador obtido da rede packageId=%s artifact=%s versao=%s",
			packageID, artifactName, strings.TrimSpace(version)))
		return path, true
	}
	return "", false
}

// customArtifactVersionMatches compara a versão anunciada pelo peer com a
// versão do catálogo. Sem qualquer das duas não há como comparar — aceita.
func customArtifactVersionMatches(announced, wanted string) bool {
	announced = strings.TrimSpace(announced)
	wanted = strings.TrimSpace(wanted)
	if announced == "" || wanted == "" {
		return true
	}
	return strings.EqualFold(announced, wanted)
}

// localP2PArtifactPathByID resolve o caminho local de um artifact já presente
// no cache P2P pelo ArtifactID (sidecar .meta), exigindo a versão esperada
// quando ela é conhecida.
func (a *App) localP2PArtifactPathByID(artifactID, version string) string {
	items, err := a.ListP2PArtifacts()
	if err != nil {
		return ""
	}
	artifactID = strings.TrimSpace(artifactID)
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(item.ArtifactID), artifactID) {
			continue
		}
		if !customArtifactVersionMatches(item.Version, version) {
			continue
		}
		if path := a.localP2PArtifactPathByName(item.ArtifactName); path != "" {
			return path
		}
	}
	return ""
}

// localP2PArtifactPathByName devolve o caminho do arquivo no diretório P2P,
// validando que existe e não está vazio.
func (a *App) localP2PArtifactPathByName(artifactName string) string {
	name := strings.TrimSpace(artifactName)
	if name == "" {
		return ""
	}
	path := filepath.Join(a.GetP2PTempDir(), filepath.Base(name))
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return ""
	}
	return path
}
