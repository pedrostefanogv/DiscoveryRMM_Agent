package app

import "testing"

func TestNormalizeServerURLRejectsLocalhost(t *testing.T) {
	t.Parallel()

	if _, err := normalizeServerURL("https://localhost:8443"); err == nil {
		t.Fatalf("expected localhost to be rejected")
	}
}

func TestNormalizeServerURLRejectsPrivateIP(t *testing.T) {
	t.Parallel()

	if _, err := normalizeServerURL("https://10.0.0.20:8443"); err == nil {
		t.Fatalf("expected private IP to be rejected")
	}
}

func TestNormalizeServerURLAcceptsPublicIPAndScrubsURL(t *testing.T) {
	t.Parallel()

	u, err := normalizeServerURL("https://8.8.8.8:8443/base/path?x=1#frag")
	if err != nil {
		t.Fatalf("expected public IP URL to be accepted, got error: %v", err)
	}
	if got := u.RawQuery; got != "" {
		t.Fatalf("expected query to be scrubbed, got %q", got)
	}
	if got := u.Fragment; got != "" {
		t.Fatalf("expected fragment to be scrubbed, got %q", got)
	}
	if got := u.Path; got != "/base/path" {
		t.Fatalf("unexpected path after normalization: %q", got)
	}
}
