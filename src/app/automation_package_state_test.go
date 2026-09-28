package app

import (
	"testing"
	"time"
)

func TestPackageInFinalStateCached_CachesDecision(t *testing.T) {
	t.Cleanup(func() { packageFinalStateCache.entries = map[string]packageFinalStateEntry{} })
	packageFinalStateCache.entries = map[string]packageFinalStateEntry{}
	now := time.Now()
	calls := 0
	compute := func() bool { calls++; return true }

	if !packageInFinalStateCached(now, "brave.brave", compute) {
		t.Fatal("esperava true (instalado)")
	}
	if got := packageInFinalStateCached(now.Add(2*time.Minute), "brave.brave", compute); !got || calls != 1 {
		t.Fatalf("cache deveria atender: final=%v calls=%d", got, calls)
	}
	if got := packageInFinalStateCached(now.Add(4*time.Minute), "brave.brave", compute); !got || calls != 2 {
		t.Fatalf("TTL expirado deveria recomputar: final=%v calls=%d", got, calls)
	}
}

func TestPackageInFinalStateCached_DoesNotCacheEmptyPackage(t *testing.T) {
	t.Cleanup(func() { packageFinalStateCache.entries = map[string]packageFinalStateEntry{} })
	packageFinalStateCache.entries = map[string]packageFinalStateEntry{}
	now := time.Now()
	if packageInFinalStateCached(now, "  ", func() bool { t.Fatal("não deveria chamar compute"); return true }) {
		t.Fatal("pacote vazio é falso")
	}
	if len(packageFinalStateCache.entries) != 0 {
		t.Fatalf("pacote vazio não deve entrar no cache: %v", packageFinalStateCache.entries)
	}
}

func TestWingetPackageIDFromArtifact(t *testing.T) {
	valid := map[string]string{
		"winget:bravebrave":   "bravebrave",
		"WINGET:Brave.Brave ": "Brave.Brave",
		"selfupdate:abc":      "",
		"name:app.exe":        "",
		"":                    "",
	}
	for in, want := range valid {
		if got := wingetPackageIDFromArtifact(in); got != want {
			t.Fatalf("wingetPackageIDFromArtifact(%q) = %q, want %q", in, got, want)
		}
	}
}
