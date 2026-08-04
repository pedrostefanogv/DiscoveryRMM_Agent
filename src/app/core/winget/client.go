package winget

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"discovery/internal/ctxutil"
	"discovery/internal/processutil"
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
	if err := validateID(id); err != nil {
		return "", err
	}
	return c.run(ctx,
		"install",
		"--id", id,
		"--silent",
		"--scope", "machine",
		"--accept-source-agreements",
		"--accept-package-agreements",
	)
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
	)
}

func (c *Client) Upgrade(ctx context.Context, id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	return c.run(ctx,
		"upgrade",
		"--id", id,
		"--silent",
		"--accept-source-agreements",
		"--accept-package-agreements",
	)
}

func (c *Client) UpgradeAll(ctx context.Context) (string, error) {
	return c.run(ctx,
		"upgrade",
		"--all",
		"--silent",
		"--scope", "machine",
		"--accept-source-agreements",
		"--accept-package-agreements",
	)
}

func (c *Client) ListInstalled(ctx context.Context) (string, error) {
	return c.run(ctx,
		"list",
	)
}

func (c *Client) ListUpgradable(ctx context.Context) (string, error) {
	return c.run(ctx,
		"upgrade",
	)
}

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	runCtx, cancel := ctxutil.WithTimeout(ctx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "winget", args...)
	processutil.HideWindow(cmd)
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
