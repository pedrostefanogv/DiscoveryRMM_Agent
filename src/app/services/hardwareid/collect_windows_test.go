//go:build windows

package hardwareid

import "testing"

func TestSanitizeSMBIOSUUID(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "canonical uppercase",
			raw:  "550E8400-E29B-41D4-A716-446655440000",
			want: "550E8400-E29B-41D4-A716-446655440000",
		},
		{
			name: "braces and newlines stripped",
			raw:  "  {550e8400-e29b-41d4-a716-446655440000}\r\n ",
			want: "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name: "non-hex punctuation stripped",
			raw:  "### 550e8400-e29b-41d4-a716-446655440000 ###\n",
			want: "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name: "empty or whitespace only",
			raw:  "  \r\n\t ",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeSMBIOSUUID(tc.raw); got != tc.want {
				t.Errorf("sanitizeSMBIOSUUID(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCollectReturnsConsistentAvailabilityFlags(t *testing.T) {
	info := collect()
	if info.TPMEKAvailable {
		if info.TPMEK == "" || info.TPMEKAlg == "" {
			t.Errorf("TPMEKAvailable=true but missing hash/alg: %+v", info)
		}
	} else if info.TPMEKError == "" {
		t.Errorf("TPMEKAvailable=false should record TPMEKError: %+v", info)
	}

	if info.SMBIOSUUIDAvailable {
		if info.SMBIOSUUID == "" {
			t.Errorf("SMBIOSUUIDAvailable=true but SMBIOSUUID is empty: %+v", info)
		}
	} else if info.SMBIOSUUIDError == "" {
		t.Errorf("SMBIOSUUIDAvailable=false should record SMBIOSUUIDError: %+v", info)
	}
}
