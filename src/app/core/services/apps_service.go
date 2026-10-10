package services

import (
	"context"
	"fmt"
	"strings"
)

const (
	packageManagerTargetSeparator = "::"
	packageManagerWinget          = "winget"
	packageManagerChocolatey      = "chocolatey"
	packageManagerChocoAlias      = "choco"
)

type WingetProvider interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	UpgradeAll(ctx context.Context) (string, error)
	ListInstalled(ctx context.Context) (string, error)
	ListUpgradable(ctx context.Context) (string, error)
	Download(ctx context.Context, id, downloadDir string) (string, error)
}

type ChocolateyProvider interface {
	Install(ctx context.Context, id string) (string, error)
	Uninstall(ctx context.Context, id string) (string, error)
	Upgrade(ctx context.Context, id string) (string, error)
	ListUpgradable(ctx context.Context) (string, error)
	ListInstalled(ctx context.Context) (string, error)
}

type AppsService struct {
	winget     WingetProvider
	chocolatey ChocolateyProvider
}

func NewAppsService(winget WingetProvider, chocolatey ChocolateyProvider) *AppsService {
	return &AppsService{winget: winget, chocolatey: chocolatey}
}

// Winget retorna o provider winget para operações diretas (ex.: download sem instalar).
func (s *AppsService) Winget() WingetProvider {
	return s.winget
}

func (s *AppsService) Install(ctx context.Context, id string) (string, error) {
	return s.InstallWithSwitches(ctx, id, "", "")
}

// InstallWithSwitches instala usando os switches silenciosos do catálogo
// (silent → silentWithProgress) quando o pacote é winget. Chocolatey ignora
// os switches (usa -y próprio).
func (s *AppsService) InstallWithSwitches(ctx context.Context, id, silent, silentWithProgress string) (string, error) {
	manager, packageID, err := s.resolvePackageTarget(id)
	if err != nil {
		return "", err
	}
	if manager == packageManagerChocolatey {
		return s.chocolatey.Install(ctx, packageID)
	}
	if wp, ok := s.winget.(interface {
		InstallWithSwitches(context.Context, string, string, string) (string, error)
	}); ok {
		return wp.InstallWithSwitches(ctx, packageID, silent, silentWithProgress)
	}
	return s.winget.Install(ctx, packageID)
}

// InstallAsUser instala SEM forçar escopo de máquina, para pacotes cujo
// manifesto só oferece instalador user-scope (ex.: Brave.Brave, `Scope: user`).
// Deve ser chamado com um ctx que carregue o token da sessão interativa
// (ctxutil.WithProcessUserToken): aí o winget roda na identidade do usuário
// logado, não exige admin e o app fica no perfil dele. Chocolatey ignora
// escopo (também cai no provider winget quando disponível).
func (s *AppsService) InstallAsUser(ctx context.Context, id, silent, silentWithProgress string) (string, error) {
	manager, packageID, err := s.resolvePackageTarget(id)
	if err != nil {
		return "", err
	}
	if manager == packageManagerChocolatey {
		return s.chocolatey.Install(ctx, packageID)
	}
	if wp, ok := s.winget.(interface {
		InstallWithScope(context.Context, string, string, string, string) (string, error)
	}); ok {
		return wp.InstallWithScope(ctx, packageID, silent, silentWithProgress, "")
	}
	return s.winget.Install(ctx, packageID)
}

func (s *AppsService) Uninstall(ctx context.Context, id string) (string, error) {
	manager, packageID, err := s.resolvePackageTarget(id)
	if err != nil {
		return "", err
	}
	if manager == packageManagerChocolatey {
		return s.chocolatey.Uninstall(ctx, packageID)
	}
	return s.winget.Uninstall(ctx, packageID)
}

func (s *AppsService) Upgrade(ctx context.Context, id string) (string, error) {
	return s.UpgradeWithSwitches(ctx, id, "", "")
}

// UpgradeWithSwitches atualiza usando os switches silenciosos do catálogo
// (silent → silentWithProgress) quando o provider winget os suportar.
// Chocolatey ignora switches (usa -y próprio).
func (s *AppsService) UpgradeWithSwitches(ctx context.Context, id, silent, silentWithProgress string) (string, error) {
	manager, packageID, err := s.resolvePackageTarget(id)
	if err != nil {
		return "", err
	}
	if manager == packageManagerChocolatey {
		return s.chocolatey.Upgrade(ctx, packageID)
	}
	if wp, ok := s.winget.(interface {
		UpgradeWithSwitches(context.Context, string, string, string) (string, error)
	}); ok {
		return wp.UpgradeWithSwitches(ctx, packageID, silent, silentWithProgress)
	}
	return s.winget.Upgrade(ctx, packageID)
}

func (s *AppsService) UpgradeAll(ctx context.Context) (string, error) {
	return s.winget.UpgradeAll(ctx)
}

// ListInstalled mantém o formato TABULAR histórico: é a saída crua exposta à UI
// (aba de instalados) e às ferramentas MCP, que a exibem como texto.
func (s *AppsService) ListInstalled(ctx context.Context) (string, error) {
	return s.winget.ListInstalled(ctx)
}

func (s *AppsService) ListUpgradable(ctx context.Context) (string, error) {
	return s.winget.ListUpgradable(ctx)
}

// installedListRich e upgradableListRich são capacidades opcionais do provider:
// o client real expõe variantes que tentam "--output json" (Ids completos, sem
// truncamento de coluna) e caem para a tabela em versões sem suporte. Mocks e
// providers antigos continuam funcionando pelo caminho tabular.
type installedListRich interface {
	ListInstalledRich(ctx context.Context) (string, error)
}

type upgradableListRich interface {
	ListUpgradableRich(ctx context.Context) (string, error)
}

// ListInstalledRich é para consumidores INTERNOS que parseiam os dois formatos
// (decisão de instalação, correlação de inventário) — nunca para exibição crua.
func (s *AppsService) ListInstalledRich(ctx context.Context) (string, error) {
	if rich, ok := s.winget.(installedListRich); ok {
		return rich.ListInstalledRich(ctx)
	}
	return s.winget.ListInstalled(ctx)
}

func (s *AppsService) ListUpgradableRich(ctx context.Context) (string, error) {
	if rich, ok := s.winget.(upgradableListRich); ok {
		return rich.ListUpgradableRich(ctx)
	}
	return s.winget.ListUpgradable(ctx)
}

func (s *AppsService) ListUpgradableChocolatey(ctx context.Context) (string, error) {
	if s.chocolatey == nil {
		return "", nil
	}
	out, err := s.chocolatey.ListUpgradable(ctx)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "chocolatey nao encontrado") {
		return "", nil
	}
	return out, err
}

// ListInstalledChocolatey lista os pacotes locais do Chocolatey, tolerando a
// ausência do choco no host (retorna vazio em vez de erro).
func (s *AppsService) ListInstalledChocolatey(ctx context.Context) (string, error) {
	if s.chocolatey == nil {
		return "", nil
	}
	out, err := s.chocolatey.ListInstalled(ctx)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "chocolatey nao encontrado") {
		return "", nil
	}
	return out, err
}

func (s *AppsService) resolvePackageTarget(raw string) (string, string, error) {
	manager, packageID := splitPackageManagerTarget(raw)
	if packageID == "" {
		return "", "", fmt.Errorf("id do pacote e obrigatorio")
	}
	if manager == packageManagerChocolatey && s.chocolatey == nil {
		return "", "", fmt.Errorf("Chocolatey nao encontrado no host")
	}
	if manager == packageManagerWinget || manager == "" {
		return packageManagerWinget, packageID, nil
	}
	if manager == packageManagerChocolatey {
		return packageManagerChocolatey, packageID, nil
	}
	return "", "", fmt.Errorf("gerenciador de pacote invalido: %s", manager)
}

func splitPackageManagerTarget(raw string) (string, string) {
	target := strings.TrimSpace(raw)
	parts := strings.SplitN(target, packageManagerTargetSeparator, 2)
	if len(parts) != 2 {
		return "", target
	}
	manager := normalizePackageManager(parts[0])
	packageID := strings.TrimSpace(parts[1])
	if manager == "" {
		return "", target
	}
	return manager, packageID
}

func normalizePackageManager(raw string) string {
	manager := strings.ToLower(strings.TrimSpace(raw))
	switch manager {
	case packageManagerWinget:
		return packageManagerWinget
	case packageManagerChocolatey, packageManagerChocoAlias:
		return packageManagerChocolatey
	default:
		return ""
	}
}
