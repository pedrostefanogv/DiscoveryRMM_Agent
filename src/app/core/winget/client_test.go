package winget

import (
	"errors"
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

func TestShouldTreatErrorAsSuccess(t *testing.T) {
	err := errors.New("exit status 0x8a15002b")

	if !shouldTreatErrorAsSuccess([]string{"upgrade", "--id", "Mozilla.Firefox"}, "", err) {
		t.Fatal("upgrade com codigo benigno do winget deveria ser tratado como sucesso")
	}

	output := "Foi encontrado um pacote existente ja instalado. Tentando atualizar o pacote instalado...\nNenhuma atualiza"
	if !shouldTreatErrorAsSuccess([]string{"install", "--id", "Mozilla.Firefox"}, output, errors.New("falha localizada")) {
		t.Fatal("install sem atualizacao disponivel deveria ser tratado como sucesso")
	}

	if shouldTreatErrorAsSuccess([]string{"uninstall", "--id", "Mozilla.Firefox"}, output, err) {
		t.Fatal("uninstall nao deve mascarar erros benignos de upgrade")
	}

	if shouldTreatErrorAsSuccess([]string{"upgrade", "--id", "Mozilla.Firefox"}, "erro real", errors.New("exit status 1")) {
		t.Fatal("erro real de upgrade nao deve ser tratado como sucesso")
	}
}

func TestHasNoopUpgradeOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "portuguese no updates",
			output: "Nenhuma atualizacao disponivel foi encontrada.\nNenhuma versao de pacote mais recente esta disponivel nas origens configuradas.",
			want:   true,
		},
		{
			name:   "english no updates",
			output: "No available upgrade found.\nNo newer package versions are available from the configured sources.",
			want:   true,
		},
		{
			name:   "actual failure",
			output: "The source requires that you view the following agreements before using.",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasNoopUpgradeOutput(tc.output); got != tc.want {
				t.Fatalf("hasNoopUpgradeOutput() = %v, want %v", got, tc.want)
			}
		})
	}
}
