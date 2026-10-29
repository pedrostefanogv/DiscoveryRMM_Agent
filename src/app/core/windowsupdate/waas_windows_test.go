//go:build windows

package windowsupdate

import "testing"

func TestParseWaaSFeatureAssessment(t *testing.T) {
	raw := `{"nextApplicableReleaseTime":0,"isOutOfDate":true,"isEndOfSupport":false,"status":1,"impact":2,"daysOutOfDate":10,"upToDateOSBuild":"10.0.26300.1"}`
	assessment, ok := parseWaaSFeatureAssessment(raw)
	if !ok {
		t.Fatal("parse falhou")
	}
	if !assessment.IsOutOfDate || assessment.Status != 1 || assessment.Impact != 2 || assessment.DaysOutOfDate != 10 {
		t.Fatalf("campos inesperados: %+v", assessment)
	}
	if assessment.UpToDateOSBuild != "10.0.26300.1" {
		t.Fatalf("upToDateOSBuild=%q", assessment.UpToDateOSBuild)
	}

	if _, ok := parseWaaSFeatureAssessment(""); ok {
		t.Fatal("vazio deveria falhar")
	}
	if _, ok := parseWaaSFeatureAssessment("{"); ok {
		t.Fatal("json invalido deveria falhar")
	}
}

func TestWaaSFeatureUpdateAvailable(t *testing.T) {
	cases := []struct {
		name string
		a    waasFeatureAssessment
		want bool
	}{
		{"latest", waasFeatureAssessment{Status: 0}, false},
		{"outOfDate", waasFeatureAssessment{IsOutOfDate: true}, true},
		{"deferredFeature", waasFeatureAssessment{Status: 6}, true},
		{"endOfSupport", waasFeatureAssessment{IsEndOfSupport: true, Status: 0}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.featureUpdateAvailable(); got != tc.want {
				t.Fatalf("featureUpdateAvailable=%v want %v (%+v)", got, tc.want, tc.a)
			}
		})
	}
}

func TestAppendWaaSFeatureUpdate(t *testing.T) {
	available := waasFeatureAssessment{IsOutOfDate: true, Status: 1, Impact: 2}
	updates := appendWaaSFeatureUpdate(nil, available, "25H2")
	if len(updates) != 1 {
		t.Fatalf("esperava 1 update, veio %d", len(updates))
	}
	u := updates[0]
	if u.Source != "waas" || u.UpdateKey != waasFeatureUpdateKey || u.UpdateType != "feature" {
		t.Fatalf("update sintetico inesperado: %+v", u)
	}
	if !u.IsPending || u.IsInstalled {
		t.Fatalf("item deveria ser pendente: %+v", u)
	}

	// Ja existe um upgrade vindo do WUA: nao duplica.
	withUpgrade := []Update{{Title: "Windows 11, version 26H2", Classification: "Upgrades", UpdateType: "feature"}}
	if got := appendWaaSFeatureUpdate(withUpgrade, available, "25H2"); len(got) != 1 {
		t.Fatalf("nao deveria duplicar upgrade: %d", len(got))
	}

	// Assessment "latest": nao acrescenta nada.
	if got := appendWaaSFeatureUpdate(nil, waasFeatureAssessment{Status: 0}, "25H2"); len(got) != 0 {
		t.Fatalf("latest nao deveria sintetizar: %d", len(got))
	}
}
