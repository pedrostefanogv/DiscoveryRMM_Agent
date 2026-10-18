package windowsupdate

import "testing"

func TestNormalizeGUID(t *testing.T) {
	cases := []struct{ in, want string }{
		{"{3C3F1F1A-1111-2222-3333-444455556666}", "3c3f1f1a-1111-2222-3333-444455556666"},
		{"3c3f1f1a-1111-2222-3333-444455556666", "3c3f1f1a-1111-2222-3333-444455556666"},
		{"", ""},
		{"not-a-guid", ""},
		{"3c3f1f1a-1111-2222-3333", ""},
	}
	for _, c := range cases {
		if got := NormalizeGUID(c.in); got != c.want {
			t.Fatalf("NormalizeGUID(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeKB(t *testing.T) {
	cases := []struct{ in, want string }{
		{"5034441", "KB5034441"},
		{"kb5034441", "KB5034441"},
		{" KB 5034441 ", "KB5034441"},
		{"KB005034441", "KB5034441"},
		{"", ""},
		{"KB", ""},
		{"KBABC", ""},
		{"KB12", ""},
	}
	for _, c := range cases {
		if got := NormalizeKB(c.in); got != c.want {
			t.Fatalf("NormalizeKB(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeKBIDs(t *testing.T) {
	got := NormalizeKBIDs([]string{"5034441", "KB5034441", "kb 5034100", "lixo"})
	if len(got) != 2 || got[0] != "KB5034441" || got[1] != "KB5034100" {
		t.Fatalf("NormalizeKBIDs=%v", got)
	}
}

func TestGuidKeyAndParse(t *testing.T) {
	key := GuidKey("{3C3F1F1A-1111-2222-3333-444455556666}", 12)
	if key != "guid:3c3f1f1a-1111-2222-3333-444455556666:12" {
		t.Fatalf("GuidKey=%q", key)
	}
	isGUID, id, rev, ok := ParseUpdateKey(key)
	if !ok || !isGUID || id != "3c3f1f1a-1111-2222-3333-444455556666" || rev != 12 {
		t.Fatalf("ParseUpdateKey=%v %v %v %v", isGUID, id, rev, ok)
	}
	if GuidKey("", 1) != "" {
		t.Fatal("GuidKey invalido deveria ser vazio")
	}
}

func TestKBKeyAndParse(t *testing.T) {
	key := KBKey("kb5034441")
	if key != "kb:KB5034441" {
		t.Fatalf("KBKey=%q", key)
	}
	isGUID, id, rev, ok := ParseUpdateKey(key)
	if !ok || isGUID || id != "KB5034441" || rev != 0 {
		t.Fatalf("ParseUpdateKey=%v %v %v %v", isGUID, id, rev, ok)
	}
}

func TestUpdateKeyFor(t *testing.T) {
	if got := UpdateKeyFor("3c3f1f1a-1111-2222-3333-444455556666", 1, []string{"KB1"}); got == "" {
		t.Fatal("esperava chave guid")
	}
	if got := UpdateKeyFor("", 0, []string{"5034441"}); got != "kb:KB5034441" {
		t.Fatalf("fallback kb=%q", got)
	}
	if got := UpdateKeyFor("", 0, nil); got != "" {
		t.Fatalf("esperava vazio, got %q", got)
	}
}

func TestValidUpdateKey(t *testing.T) {
	for _, key := range []string{
		"guid:3c3f1f1a-1111-2222-3333-444455556666:3",
		"kb:KB5034441",
	} {
		if !ValidUpdateKey(key) {
			t.Fatalf("chave %q deveria ser valida", key)
		}
	}
	for _, key := range []string{"", "guid:", "guid:abc:1", "kb:", "kb:xx", "KB5034441", "guid:3c3f1f1a-1111-2222-3333-444455556666"} {
		if ValidUpdateKey(key) {
			t.Fatalf("chave %q deveria ser invalida", key)
		}
	}
}
