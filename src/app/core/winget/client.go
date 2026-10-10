package winget

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"discovery/app/core/ctxutil"
	"discovery/app/core/processutil"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

const wingetNoApplicableUpgradeCode = "0x8a15002b"

type Client struct {
	timeout time.Duration
}

func NewClient(timeout time.Duration) *Client {
	return &Client{timeout: timeout}
}

func (c *Client) Install(ctx context.Context, id string) (string, error) {
	return c.InstallWithSwitches(ctx, id, "", "")
}

// InstallWithSwitches instala via winget anexando os switches silenciosos do
// catálogo (campo "silent", fallback "silentWithProgress") via --custom.
// --custom ADICIONA aos switches padrão do winget (preserva --accept-*-agreements),
// ao contrário de --override que os substituiria. Switches vazios = comportamento padrão.
func (c *Client) InstallWithSwitches(ctx context.Context, id, silent, silentWithProgress string) (string, error) {
	return c.InstallWithScope(ctx, id, silent, silentWithProgress, "machine")
}

// InstallWithScope instala com um escopo explícito. scope VAZIO omite --scope
// e deixa o winget escolher o instalador aplicável do manifesto.
//
// Usado para pacotes cujo manifesto só oferece instalador USER-scope (ex.:
// Brave.Brave declara `Scope: user`): `--scope machine` não tem instalador
// aplicável e a instalação falha. Esses pacotes são instalados rodando o
// winget na identidade do usuário logado (CreateProcessAsUser): não exige
// admin e o app fica no perfil do usuário.
func (c *Client) InstallWithScope(ctx context.Context, id, silent, silentWithProgress, scope string) (string, error) {
	args, err := buildInstallArgs(id, silent, silentWithProgress, scope)
	if err != nil {
		return "", err
	}
	return c.run(ctx, args...)
}

// buildInstallArgs é puro (testável sem executar winget) e monta os argumentos
// de instalação.
func buildInstallArgs(id, silent, silentWithProgress, scope string) ([]string, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	sw := strings.TrimSpace(silent)
	if sw == "" {
		sw = strings.TrimSpace(silentWithProgress)
	}
	args := []string{
		"install",
		"--id", id,
		"--silent",
	}
	if s := strings.TrimSpace(scope); s != "" {
		args = append(args, "--scope", s)
	}
	args = append(args,
		"--accept-source-agreements",
		"--accept-package-agreements",
		"--disable-interactivity",
	)
	// C7: só repassamos switches do catálogo via --custom quando eles são
	// argumentos válidos para o instalador do pacote. Switches de .exe (ex.:
	// /S do NSIS) aplicados a pacotes MSI causam "0x8a15004a: Arguments for
	// msiexec are invalid" — o winget já sabe as flags corretas do manifesto
	// quando usamos apenas --silent, então --custom só entra com switches
	// explícitos de MSI (KEY=VALUE) ou quando o pacote não é MSI.
	if sw != "" && !looksLikeExeOnlySwitch(sw) {
		args = append(args, "--custom", sw)
	}
	return args, nil
}

// looksLikeExeOnlySwitch detecta switches que só fazem sentido para
// instaladores .exe (NSIS/Inno/etc.) e quebram o msiexec quando repassados
// via --custom. MSI aceita apenas opções nativas (/qn...) e KEY=VALUE.
func looksLikeExeOnlySwitch(sw string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(sw)))
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		switch f {
		case "/s", "/silent", "/verysilent", "/sp-", "/norestart", "/supmsg", "/noicons", "/currentuser", "/allusers", "/dir", "/group", "/no space", "/type", "/components", "/tasks":
			if f == "/norestart" {
				// /norestart também é válido no msiexec — não é exe-only.
				continue
			}
			return true
		}
	}
	return false
}

func (c *Client) Uninstall(ctx context.Context, id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	return c.run(ctx,
		"uninstall",
		"--id", id,
		"--silent",
		"--scope", "machine",
		"--accept-source-agreements",
		"--disable-interactivity",
	)
}

func (c *Client) Upgrade(ctx context.Context, id string) (string, error) {
	return c.UpgradeWithSwitches(ctx, id, "", "")
}

// UpgradeWithSwitches atualiza via winget anexando os switches silenciosos do
// catálogo via --custom, com a mesma regra de segurança do InstallWithSwitches
// (switches exe-only são descartados para não quebrar pacotes MSI).
func (c *Client) UpgradeWithSwitches(ctx context.Context, id, silent, silentWithProgress string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	sw := strings.TrimSpace(silent)
	if sw == "" {
		sw = strings.TrimSpace(silentWithProgress)
	}
	args := []string{
		"upgrade",
		"--id", id,
		"--silent",
		"--accept-source-agreements",
		"--accept-package-agreements",
		"--disable-interactivity",
	}
	if sw != "" && !looksLikeExeOnlySwitch(sw) {
		args = append(args, "--custom", sw)
	}
	return c.run(ctx, args...)
}

func (c *Client) UpgradeAll(ctx context.Context) (string, error) {
	return c.run(ctx,
		"upgrade",
		"--all",
		"--silent",
		"--scope", "machine",
		"--accept-source-agreements",
		"--accept-package-agreements",
		"--disable-interactivity",
	)
}

// ListInstalled lista os pacotes instalados reconhecidos pelo winget.
//
// As flags de aceite NÃO são cosméticas: o agente roda como SERVIÇO (LocalSystem)
// e o winget pede confirmação dos termos da fonte "msstore" na primeira vez que
// a fonte é usada por aquele contexto. Sem terminal, o prompt falha com
//
//	0x8a150042 : Error reading input in prompt
//
// e a listagem inteira volta vazia — o inventário é reportado sem os Ids de
// pacote (update/desinstalação deixam de funcionar). Como usuário interativo o
// prompt costuma não aparecer (termos já aceitos no perfil), o que torna a falha
// invisível em teste manual.
func (c *Client) ListInstalled(ctx context.Context) (string, error) {
	return c.run(ctx,
		"list",
		"--accept-source-agreements",
		"--disable-interactivity",
	)
}

// ListUpgradable lista os pacotes com atualização disponível.
// Mesmas flags de aceite do ListInstalled (ver comentário acima): sem elas o
// scan de updates devolve vazio no contexto do serviço.
func (c *Client) ListUpgradable(ctx context.Context) (string, error) {
	return c.run(ctx,
		"upgrade",
		"--accept-source-agreements",
		"--disable-interactivity",
	)
}

// Download baixa o instalador sem executá-lo, retornando a saída do winget.
// O diretório de download é controlado por --download-directory.
func (c *Client) Download(ctx context.Context, id, downloadDir string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	return c.run(ctx,
		"download",
		"--id", id,
		"--download-directory", downloadDir,
		"--accept-source-agreements",
		"--accept-package-agreements",
		"--disable-interactivity",
	)
}

// wingetRunSem serializa as invocacoes do winget no processo.
//
// NÃO é reentrante: nenhum método do Client pode ser chamado de dentro de outro
// (nem de dentro de run), sob pena de deadlock permanente. Hoje a única origem
// de execução do winget é run(); core/winget/resolve.go só usa os.Stat e
// PowerShell/registro, nunca o binário winget. Chamadas
// concorrentes (a automacao dispara install enquanto o preload roda download e
// a UI roda list/upgrade) disputam o cache de fontes do winget e falham com
// 0x8a150001 (APPINSTALLER_CLI_ERROR_INTERNAL_ERROR) — observado em campo
// exatamente durante instalacao + preload simultaneos. Serializar elimina a
// classe de falha sem custo funcional (o winget ja e lento por natureza).
var wingetRunSem = make(chan struct{}, 1)

// acquireWingetRun reserva a vez respeitando o ctx (nao bloqueia indefinidamente
// quando a execucao tem prazo).
func acquireWingetRun(ctx context.Context) error {
	select {
	case wingetRunSem <- struct{}{}:
		return nil
	default:
	}
	select {
	case wingetRunSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseWingetRun() { <-wingetRunSem }

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	if err := acquireWingetRun(ctx); err != nil {
		return "", err
	}
	defer releaseWingetRun()

	runCtx, cancel := ctxutil.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := c.command(runCtx, args...)
	processutil.HideWindow(cmd)
	// Honra um token de usuário colocado no ctx (sessão interativa): sem isso,
	// `winget list/install` rodam sempre como SYSTEM e não enxergam (nem
	// instalam em) o perfil do usuário logado — necessário para pacotes
	// user-scope (ex.: Brave.Brave, `Scope: user`).
	processutil.ApplyUserContext(runCtx, cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if shouldTreatErrorAsSuccess(args, text, err) {
			return text, nil
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("erro executando winget %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

func shouldTreatErrorAsSuccess(args []string, output string, err error) bool {
	if len(args) == 0 || err == nil {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	if command != "install" && command != "upgrade" {
		return false
	}
	errText := strings.ToLower(err.Error())
	if strings.Contains(errText, wingetNoApplicableUpgradeCode) {
		return true
	}
	return hasNoopUpgradeOutput(output)
}

func hasNoopUpgradeOutput(output string) bool {
	normalized := strings.ToLower(strings.TrimSpace(output))
	if normalized == "" {
		return false
	}
	markers := []string{
		"nenhuma atualiza",
		"nenhuma vers",
		"no available upgrade found",
		"no newer package versions are available",
	}
	for _, marker := range markers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func validateID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id do pacote e obrigatorio")
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("id do pacote invalido")
	}
	return nil
}
