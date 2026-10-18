//go:build !windows

package windowsupdate

import (
	"context"
	"time"
)

type unsupportedClient struct {
	opts Options
}

func newPlatformClient(opts Options) Client {
	return &unsupportedClient{opts: opts}
}

func (c *unsupportedClient) Probe(context.Context) ProbeInfo {
	return ProbeInfo{Supported: false, ErrorCode: ScanErrorUnsupported}
}

func (c *unsupportedClient) Scan(context.Context, ScanOptions) (*ScanResult, error) {
	return nil, ErrUnsupportedOS
}

func (c *unsupportedClient) Install(context.Context, []InstallTarget) (*InstallResult, error) {
	return nil, ErrUnsupportedOS
}

func (c *unsupportedClient) Hide(context.Context, []string, bool) (int, error) {
	return 0, ErrUnsupportedOS
}

func probePendingReboot() RebootInfo {
	return RebootInfo{Pending: false, CheckedAt: time.Now().UTC()}
}

func readPolicy() (PolicySettings, error) {
	return PolicySettings{}, ErrUnsupportedOS
}

func applyPolicy(PolicySettings) (*PolicyBackup, error) {
	return nil, ErrUnsupportedOS
}

func restorePolicy(PolicyBackup) error {
	return ErrUnsupportedOS
}
