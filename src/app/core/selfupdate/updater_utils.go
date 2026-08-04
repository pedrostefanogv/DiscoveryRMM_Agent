package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"discovery/internal/processutil"
)

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return strings.ToLower(hex.EncodeToString(h.Sum(nil))), nil
}

func backoffForFailures(failures int) time.Duration {
	if failures <= 1 {
		return backoffFirstFailure
	}
	if failures == 2 {
		return backoffSecondFailure
	}
	return backoffThirdOrGreater
}

func normalizeArtifactType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return artifactInstaller
	}
	if strings.EqualFold(value, artifactInstaller) {
		return artifactInstaller
	}
	return value
}

func ptrStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func manifestFileLogDetail(m *UpdateManifest) string {
	if m == nil {
		return ""
	}
	parts := []string{}
	if m.FileName != nil && strings.TrimSpace(*m.FileName) != "" {
		parts = append(parts, fmt.Sprintf("file=%s", *m.FileName))
	}
	if m.SizeBytes != nil && *m.SizeBytes > 0 {
		parts = append(parts, fmt.Sprintf("size=%d", *m.SizeBytes))
	}
	if m.Sha256 != nil && strings.TrimSpace(*m.Sha256) != "" {
		s := strings.TrimSpace(*m.Sha256)
		if len(s) > 12 {
			s = s[:12] + "..."
		}
		parts = append(parts, fmt.Sprintf("sha256=%s", s))
	}
	if m.ArtifactType != "" {
		parts = append(parts, fmt.Sprintf("artifact=%s", m.ArtifactType))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("(%s)", strings.Join(parts, " "))
}

func validateAuthenticodeSignature(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, signatureTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		"powershell",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		"$sig = Get-AuthenticodeSignature -LiteralPath $args[0]; if ($null -eq $sig) { Write-Output 'UnknownError'; exit 3 }; Write-Output $sig.Status",
		path,
	)
	processutil.HideWindow(cmd)
	out, err := cmd.CombinedOutput()
	status := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return errors.New("timeout ao validar assinatura Authenticode")
	}
	if err != nil {
		if status == "" {
			status = err.Error()
		}
		return fmt.Errorf("falha ao validar assinatura Authenticode: %s", status)
	}
	if !strings.EqualFold(status, "Valid") {
		return fmt.Errorf("assinatura Authenticode invalida: %s", status)
	}
	return nil
}

func compareVersions(a, b string) int {
	ap := parseVersionTriplet(a)
	bp := parseVersionTriplet(b)
	for i := 0; i < 3; i++ {
		if ap[i] > bp[i] {
			return 1
		}
		if ap[i] < bp[i] {
			return -1
		}
	}
	return 0
}

func parseVersionTriplet(value string) [3]int {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ".")
	result := [3]int{0, 0, 0}
	for i := 0; i < 3; i++ {
		if i >= len(parts) {
			break
		}
		result[i] = parseLeadingInt(parts[i])
	}
	return result
}

func parseLeadingInt(part string) int {
	part = strings.TrimSpace(part)
	if part == "" {
		return 0
	}
	var b strings.Builder
	for _, r := range part {
		if r < '0' || r > '9' {
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return 0
	}
	v, err := strconv.Atoi(b.String())
	if err != nil {
		return 0
	}
	return v
}
