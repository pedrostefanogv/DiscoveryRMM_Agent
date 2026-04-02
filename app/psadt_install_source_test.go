package app

import (
	"strings"
	"testing"
)

func TestParsePSADTInstallSource(t *testing.T) {
	type tc struct {
		in        string
		wantType  string
		wantValue string
	}
	cases := []tc{
		{in: "", wantType: "powershell_gallery", wantValue: ""},
		{in: "psgallery", wantType: "powershell_gallery", wantValue: ""},
		{in: "internal:CorpRepo", wantType: "internal", wantValue: "CorpRepo"},
		{in: "offline:C:/repo/PSAppDeployToolkit", wantType: "offline", wantValue: "C:/repo/PSAppDeployToolkit"},
	}

	for _, item := range cases {
		gotType, gotValue := parsePSADTInstallSource(item.in)
		if gotType != item.wantType || gotValue != item.wantValue {
			t.Fatalf("parse source %q => (%q, %q), expected (%q, %q)", item.in, gotType, gotValue, item.wantType, item.wantValue)
		}
	}
}

func TestBuildPSADTInstallScript_BySource(t *testing.T) {
	internalScript := buildPSADTInstallScript("4.1.8", "internal", "CorpRepo")
	if !strings.Contains(internalScript, "-Repository 'CorpRepo'") {
		t.Fatalf("expected internal source script to include repository")
	}

	offlineScript := buildPSADTInstallScript("4.1.8", "offline", "C:/repo/PSAppDeployToolkit")
	if !strings.Contains(offlineScript, "offline source nao encontrada") {
		t.Fatalf("expected offline source script validation")
	}

	galleryScript := buildPSADTInstallScript("4.1.8", "powershell_gallery", "")
	if !strings.Contains(galleryScript, "Install-Module -Name PSAppDeployToolkit") {
		t.Fatalf("expected PSGallery script to install module")
	}
}
