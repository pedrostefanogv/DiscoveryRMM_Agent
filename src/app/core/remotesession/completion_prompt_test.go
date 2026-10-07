//go:build windows

package remotesession

import "testing"

func TestCompletionPromptPrefixLen(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"powershell default", "PS C:\\Users\\x> Get-Ch", len("PS C:\\Users\\x> ")},
		{"powershell enxuto", "PS> Get-Ch", len("PS> ")},
		{"cmd", "C:\\Users\\x>dir", len("C:\\Users\\x>")},
		{"unix user@host", "user@host:~$ ls", len("user@host:~$ ")},
		{"unix root", "root# ls", len("root# ")},
		{"bracket", "[user@host ~]$ ls", len("[user@host ~]$ ")},
		{"comando sem prompt", "Get-ChildItem", 0},
		{"variavel nao e prompt", "echo $HOME", 0},
		{"variavel no inicio", "$env:PATH", 0},
		{"comentario no inicio", "#region setup", 0},
		{"hash no meio", "foo#bar", 0},
		{"redirecionamento nao e prompt", "dir > out.txt", 0},
		{"vazio", "", 0},
	}
	for _, tc := range cases {
		if got := completionPromptPrefixLen(tc.input); got != tc.want {
			t.Fatalf("%s: completionPromptPrefixLen(%q) = %d, want %d", tc.name, tc.input, got, tc.want)
		}
	}
}

func TestSanitizeCompletionInput(t *testing.T) {
	// "PS C:\> " tem 8 bytes/runas; cursor 12 -> 4 no texto sem prompt.
	in, cur, prefix := sanitizeCompletionInput("PS C:\\> Get-Ch", 12)
	if in != "Get-Ch" || cur != 4 || prefix != 8 {
		t.Fatalf("sanitize = (%q,%d,%d), want (Get-Ch,4,8)", in, cur, prefix)
	}

	// Sem prompt: no-op.
	in2, cur2, prefix2 := sanitizeCompletionInput("echo $HOME", 5)
	if in2 != "echo $HOME" || cur2 != 5 || prefix2 != 0 {
		t.Fatalf("sanitize sem prompt = (%q,%d,%d), want (echo $HOME,5,0)", in2, cur2, prefix2)
	}

	// Cursor antes do fim do prompt e clampado em 0.
	_, cur3, _ := sanitizeCompletionInput("PS C:\\> Get-Ch", 3)
	if cur3 != 0 {
		t.Fatalf("cursor clampado = %d, want 0", cur3)
	}
}
