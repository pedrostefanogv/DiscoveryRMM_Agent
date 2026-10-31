//go:build windows

package windowsupdate

import (
	"fmt"
	"testing"
)

// TestLocalSizeDiag (temp): imprime o MaxDownloadSize que o agente leu.
func TestLocalSizeDiag(t *testing.T) {
	if testing.Short() {
		t.Skip("local diag")
	}
	c := newPlatformClient(DefaultOptions())
	res, err := c.Scan(nil, ScanOptions{IncludeInstalled: false})
	if err != nil {
		t.Fatalf("scan falhou: %v", err)
	}
	for _, u := range res.Updates {
		fmt.Printf("- %s | maxDownloadSize=%d\n", u.UpdateKey, u.MaxDownloadSize)
	}
}
