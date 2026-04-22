package p2pmeta

import (
	"encoding/json"
	"testing"
)

func TestSeedPlanRecommendationUnmarshalCamelCase(t *testing.T) {
	var got SeedPlanRecommendation
	raw := []byte(`{"siteId":"site-1","generatedAtUtc":"2026-03-23T12:00:00Z","plan":{"totalAgents":50,"configuredPercent":10,"minSeeds":2,"selectedSeeds":5}}`)

	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal camelCase: %v", err)
	}

	assertSeedPlanRecommendation(t, got)
}

func TestSeedPlanRecommendationUnmarshalPascalCaseCompat(t *testing.T) {
	var got SeedPlanRecommendation
	raw := []byte(`{"SiteID":"site-1","GeneratedAtUTC":"2026-03-23T12:00:00Z","Plan":{"TotalAgents":50,"ConfiguredPercent":10,"MinSeeds":2,"SelectedSeeds":5}}`)

	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("json.Unmarshal PascalCase: %v", err)
	}

	assertSeedPlanRecommendation(t, got)
}

func assertSeedPlanRecommendation(t *testing.T, got SeedPlanRecommendation) {
	t.Helper()

	if got.SiteID != "site-1" {
		t.Fatalf("SiteID = %q, want %q", got.SiteID, "site-1")
	}
	if got.GeneratedAtUTC != "2026-03-23T12:00:00Z" {
		t.Fatalf("GeneratedAtUTC = %q, want %q", got.GeneratedAtUTC, "2026-03-23T12:00:00Z")
	}
	if got.Plan.TotalAgents != 50 {
		t.Fatalf("Plan.TotalAgents = %d, want 50", got.Plan.TotalAgents)
	}
	if got.Plan.ConfiguredPercent != 10 {
		t.Fatalf("Plan.ConfiguredPercent = %d, want 10", got.Plan.ConfiguredPercent)
	}
	if got.Plan.MinSeeds != 2 {
		t.Fatalf("Plan.MinSeeds = %d, want 2", got.Plan.MinSeeds)
	}
	if got.Plan.SelectedSeeds != 5 {
		t.Fatalf("Plan.SelectedSeeds = %d, want 5", got.Plan.SelectedSeeds)
	}
}
