package chocolatey

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
	return c.run(ctx, "install", id, "-y", "--no-progress")
}

func (c *Client) Uninstall(ctx context.Context, id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	return c.run(ctx, "uninstall", id, "-y", "--no-progress")
}

func (c *Client) Upgrade(ctx context.Context, id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	return c.run(ctx, "upgrade", id, "-y", "--no-progress")
}

func (c *Client) ListUpgradable(ctx context.Context) (string, error) {
	return c.run(ctx, "outdated", "--limit-output", "--no-color")
}

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	runCtx, cancel := ctxutil.WithTimeout(ctx, c.timeout)
	defer cancel()

	if _, err := exec.LookPath("choco"); err != nil {
		return "", fmt.Errorf("Chocolatey nao encontrado no host")
	}

	cmd := exec.CommandContext(runCtx, "choco", args...)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		if shouldTreatOutdatedExitAsSuccess(args, text, err) {
			return text, nil
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("erro executando chocolatey %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

func shouldTreatOutdatedExitAsSuccess(args []string, output string, err error) bool {
	if len(args) == 0 || err == nil {
		return false
	}
	if strings.ToLower(strings.TrimSpace(args[0])) != "outdated" {
		return false
	}
	if strings.Contains(output, "|") {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(output))
	return strings.Contains(normalized, "0 package") || strings.Contains(normalized, "nenhum")
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
