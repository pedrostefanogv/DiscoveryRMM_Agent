package winget

import (
	"testing"
)

func TestValidateID_Valid(t *testing.T) {
	valid := []string{
		"Microsoft.VisualStudioCode",
		"Git.Git",
		"Mozilla.Firefox",
		"7zip.7zip",
		"some-id_with.dots",
	}
	for _, id := range valid {
		if err := validateID(id); err != nil {
			t.Errorf("validateID(%q) returned error: %v", id, err)
		}
	}
}

func TestValidateID_Empty(t *testing.T) {
	for _, id := range []string{"", "   ", "\t"} {
		if err := validateID(id); err == nil {
			t.Errorf("validateID(%q) should return error for empty/blank input", id)
		}
	}
}

func TestValidateID_Invalid(t *testing.T) {
	invalid := []string{
		"has space",
		"has;semicolon",
		"rm -rf /",
		"id&echo",
		"id|pipe",
		"$(cmd)",
		"name\x00null",
		"日本語",
	}
	for _, id := range invalid {
		if err := validateID(id); err == nil {
			t.Errorf("validateID(%q) should return error for invalid characters", id)
		}
	}
}
