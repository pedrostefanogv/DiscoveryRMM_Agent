package app

import "testing"

// TestCustomArtifactID cobre o ID canônico do instalador custom no P2P. A versão
// faz parte do ID para que um artifact de versão antiga nunca seja servido ou
// reaproveitado depois de um bump no catálogo.
func TestCustomArtifactID(t *testing.T) {
	cases := []struct {
		packageID string
		version   string
		want      string
	}{
		{"foxitedito", "2", "custom:foxitedito:v2"},
		{"Foxit.Editor", "14.0.8.33807", "custom:foxiteditor:v14.0.8.33807"},
		{"foxitedito", "", "custom:foxitedito"},
		{"foxitedito", "   ", "custom:foxitedito"},
		{"foxitedito", "v 2!", "custom:foxitedito:v2"},
		{"", "2", ""},
		{"   ", "2", ""},
	}

	for _, tc := range cases {
		if got := customArtifactID(tc.packageID, tc.version); got != tc.want {
			t.Fatalf("customArtifactID(%q, %q) = %q, esperado %q", tc.packageID, tc.version, got, tc.want)
		}
	}
}

// TestCustomArtifactIDSeparaVersoes garante que versões diferentes do mesmo app
// geram IDs diferentes (sem isso, um peer com o instalador v2 responderia à
// busca do v3).
func TestCustomArtifactIDSeparaVersoes(t *testing.T) {
	v2 := customArtifactID("foxitedito", "2")
	v3 := customArtifactID("foxitedito", "3")
	if v2 == v3 {
		t.Fatalf("versões diferentes não podem compartilhar artifactID: %q", v2)
	}
}

func TestCustomArtifactVersionMatches(t *testing.T) {
	cases := []struct {
		announced string
		wanted    string
		want      bool
	}{
		{"2", "2", true},
		{" 2 ", "2", true},
		{"2", "3", false},
		{"", "3", true}, // sem versão anunciada não há como comparar
		{"2", "", true}, // sem versão esperada não há como comparar
	}

	for _, tc := range cases {
		if got := customArtifactVersionMatches(tc.announced, tc.wanted); got != tc.want {
			t.Fatalf("customArtifactVersionMatches(%q, %q) = %v, esperado %v",
				tc.announced, tc.wanted, got, tc.want)
		}
	}
}
