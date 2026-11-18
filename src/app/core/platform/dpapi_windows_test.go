//go:build windows

package platform

import (
	"strings"
	"testing"
)

func TestProtectUnprotectSecretRoundTrip(t *testing.T) {
	if got, err := ProtectSecret(""); err != nil || got != "" {
		t.Fatalf("ProtectSecret empty = (%q, %v), want (\"\", nil)", got, err)
	}
	if got, err := UnprotectSecret("   "); err != nil || got != "" {
		t.Fatalf("UnprotectSecret blank = (%q, %v), want (\"\", nil)", got, err)
	}

	secret := "tok-super-secreto-123!@#\nlinha2"
	protected, err := ProtectSecret(secret)
	if err != nil {
		t.Fatalf("ProtectSecret: %v", err)
	}
	if !strings.HasPrefix(protected, "dpapi:v1:") {
		t.Fatalf("expected dpapi:v1: prefix, got %q", protected)
	}
	if !IsProtectedSecret(protected) {
		t.Fatalf("IsProtectedSecret should be true")
	}
	if strings.Contains(protected, secret) {
		t.Fatalf("protected output must not contain plaintext")
	}

	recovered, err := UnprotectSecret(protected)
	if err != nil {
		t.Fatalf("UnprotectSecret: %v", err)
	}
	if recovered != secret {
		t.Fatalf("recovered = %q, want %q", recovered, secret)
	}

	if _, err := UnprotectSecret("plaintext-without-prefix"); err == nil {
		t.Fatalf("expected error for missing dpapi:v1: prefix")
	}
	if _, err := UnprotectSecret("dpapi:v1:!!!not-base64!!!"); err == nil {
		t.Fatalf("expected error for invalid base64")
	}
	if _, err := UnprotectSecret("dpapi:v1:YWJjZGVm"); err == nil {
		t.Fatalf("expected error for tampered ciphertext")
	}
}
