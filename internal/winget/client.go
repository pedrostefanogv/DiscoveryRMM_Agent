package winget

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"winget-store/internal/ctxutil"
	"winget-store/internal/processutil"
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
	return c.run(ctx,
		"install",
		"--id", id,
		"--silent",
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
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("erro executando winget %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
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
