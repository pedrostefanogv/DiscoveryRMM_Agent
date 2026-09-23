package app

import "testing"

func TestIsSoftwareUninstallCommandType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"softwareuninstall", true},
		{"SoftwareUninstall", true},
		{"software-uninstall", true},
		{"software_update", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsSoftwareUninstallCommandType(tc.in); got != tc.want {
			t.Fatalf("IsSoftwareUninstallCommandType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsMsiProductCode(t *testing.T) {
	valid := "{26A24AE4-039D-4CA4-87B4-2F83218035F0}"
	if !isMsiProductCode(valid) {
		t.Fatalf("esperava %q válido", valid)
	}
	for _, invalid := range []string{
		"",
		"26A24AE4-039D-4CA4-87B4-2F83218035F0",
		"{26A24AE4-039D-4CA4-87B4-2F83218035F0",
		"{ZZZZZZZZ-039D-4CA4-87B4-2F83218035F0}",
		"{26A24AE4-039D-4CA4-87B4-2F83218035F0X}",
	} {
		if isMsiProductCode(invalid) {
			t.Fatalf("nao esperava %q valido", invalid)
		}
	}
}

func TestLooksLikeUninstallCommand(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"MsiExec.exe /X{26A24AE4-039D-4CA4-87B4-2F83218035F0}", true},
		{"app-uninstall.exe /S", true},
		{"/opt/app", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := looksLikeUninstallCommand(tc.in); got != tc.want {
			t.Fatalf("looksLikeUninstallCommand(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
