package app

import "testing"

func TestIsSoftwareUpdateCommandType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"softwareupdate", true},
		{"SoftwareUpdate", true},
		{"software-update", true},
		{"software_update", true},
		{" powershell ", false},
		{"systeminfo", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsSoftwareUpdateCommandType(tc.in); got != tc.want {
			t.Fatalf("IsSoftwareUpdateCommandType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
