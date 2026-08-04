//go:build windows

package processutil

import (
	"testing"
	"unicode/utf16"
)

// makeEnvBlock builds a Windows-style multi-string UTF-16 environment block
// from a list of "KEY=VALUE" strings.
func makeEnvBlock(entries ...string) []uint16 {
	var buf []uint16
	for _, e := range entries {
		buf = append(buf, utf16.Encode([]rune(e))...)
		buf = append(buf, 0) // null terminator for this entry
	}
	buf = append(buf, 0) // double-null — end of block
	return buf
}

func TestParseEnvBlock_NilBlock(t *testing.T) {
	got := parseEnvBlock(nil)
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestParseEnvBlock_EmptyBlock(t *testing.T) {
	buf := makeEnvBlock() // only the double-null terminator
	got := parseEnvBlock(&buf[0])
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestParseEnvBlock_SingleEntry(t *testing.T) {
	buf := makeEnvBlock("KEY=value")
	got := parseEnvBlock(&buf[0])
	if len(got) != 1 || got[0] != "KEY=value" {
		t.Fatalf("expected [KEY=value], got %v", got)
	}
}

func TestParseEnvBlock_MultipleEntries(t *testing.T) {
	buf := makeEnvBlock("A=1", "B=hello world", "PATH=C:\\Windows\\System32")
	got := parseEnvBlock(&buf[0])
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(got), got)
	}
	if got[0] != "A=1" || got[1] != "B=hello world" || got[2] != "PATH=C:\\Windows\\System32" {
		t.Errorf("unexpected entries: %v", got)
	}
}

func TestParseEnvBlock_EntryWithEmptyValue(t *testing.T) {
	buf := makeEnvBlock("EMPTY=", "NEXT=ok")
	got := parseEnvBlock(&buf[0])
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
	}
	if got[0] != "EMPTY=" || got[1] != "NEXT=ok" {
		t.Errorf("unexpected entries: %v", got)
	}
}

func TestParseEnvBlock_UnicodeEntry(t *testing.T) {
	buf := makeEnvBlock("USER=pedro", "DESC=çãõ")
	got := parseEnvBlock(&buf[0])
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got[0] != "USER=pedro" || got[1] != "DESC=çãõ" {
		t.Errorf("unexpected entries: %v", got)
	}
}
