package windowsupdate

import "testing"

func TestResolveSeverityVendorWins(t *testing.T) {
	cases := []struct{ msrc, wantSev, wantSrc string }{
		{"Critical", "Critical", SeveritySourceVendor},
		{"important", "Important", SeveritySourceVendor},
		{"MODERATE", "Moderate", SeveritySourceVendor},
		{" low ", "Low", SeveritySourceVendor},
	}
	for _, tc := range cases {
		sev, src := ResolveSeverity(tc.msrc, []string{"Security Updates"}, "whatever")
		if sev != tc.wantSev || src != tc.wantSrc {
			t.Errorf("ResolveSeverity(%q) = %q,%q; want %q,%q", tc.msrc, sev, src, tc.wantSev, tc.wantSrc)
		}
	}
}

func TestResolveSeverityDerivedNeverCritical(t *testing.T) {
	cases := []struct {
		name     string
		cats     []string
		title    string
		wantSev  string
		wantSrc  string
	}{
		{"critical updates capped", []string{"Critical Updates"}, "Some fix", "Important", SeveritySourceDerived},
		{"security updates", []string{"Security Updates"}, "Security Update for Windows", "Important", SeveritySourceDerived},
		{"cumulative", []string{"Updates"}, "2026-10 Cumulative Update for Windows 11", "Important", SeveritySourceDerived},
		{"preview cumulative not rated", []string{"Updates"}, "2026-10 Preview Cumulative Update for Windows 11", "", SeveritySourceUnknown},
		{"rollup without security", []string{"Update Rollups"}, "Rollup for .NET", "", SeveritySourceUnknown},
		{"rollup with security", []string{"Update Rollups"}, "Security Monthly Quality Rollup", "Important", SeveritySourceDerived},
		{"security update in title", []string{"Updates"}, "Security Update for Adobe Flash", "Important", SeveritySourceDerived},
	}
	for _, tc := range cases {
		sev, src := ResolveSeverity("", tc.cats, tc.title)
		if sev != tc.wantSev || src != tc.wantSrc {
			t.Errorf("%s: ResolveSeverity = %q,%q; want %q,%q", tc.name, sev, src, tc.wantSev, tc.wantSrc)
		}
	}
}

func TestResolveSeverityUnratedClassifications(t *testing.T) {
	for _, cat := range []string{"Definition Updates", "Drivers", "Feature Packs", "Tools", "Upgrades"} {
		sev, src := ResolveSeverity("", []string{cat, "Security Updates"}, "Security Intelligence Update for Defender")
		if sev != "" || src != SeveritySourceUnknown {
			t.Errorf("classificacao %q deveria ficar sem rating; got %q,%q", cat, sev, src)
		}
	}
}

func TestResolveSeverityEmptyInputs(t *testing.T) {
	sev, src := ResolveSeverity("", nil, "")
	if sev != "" || src != SeveritySourceUnknown {
		t.Fatalf("sem dados = %q,%q; want \"\",%q", sev, src, SeveritySourceUnknown)
	}
}
