package automation

import "testing"

func TestIsPackageInOutput(t *testing.T) {
	// Saída real do `winget list` (coluna Id delimitada por whitespace).
	wingetListOutput := `Name                                                           Id                                    Version              Available
----------------------------------------------------------------------------------------------------------------------------------------
7-Zip 26.02 (x64)                                              7zip.7zip                             26.02
App Installer                                                  Microsoft.AppInstaller                1.30.69.0
Foxit Reader                                                   Foxit.FoxitReader                     12.1.0
balenaEtcher                                                   Balena.Etcher                         2.1.6`

	cases := []struct {
		name      string
		output    string
		packageID string
		want      bool
	}{
		{
			name:      "match exato do ID com pontos",
			output:    wingetListOutput,
			packageID: "Foxit.FoxitReader",
			want:      true,
		},
		{
			// Cenário do bug original: "Foxit" como substring de "FoxitReader" (sem ponto).
			// A regex com \b falhava aqui porque \b trata "." como boundary.
			// Com whitespace-boundary, "FoxitReader" não contém "Foxit" como token.
			name:      "nao deve dar falso positivo em substring sem delimitador",
			output:    "FoxitReader 12.1.0\n",
			packageID: "Foxit",
			want:      false,
		},
		{
			// Cenário real: "Foxit" aparece na coluna Name como "Foxit Reader".
			// Isso é tecnicamente um match (a string existe na saída), mas é aceitável
			// porque o usuário que busca "Foxit" provavelmente quer o Foxit Reader.
			// O bug original era "Foxit" batendo em "FoxitReader" (sem separador).
			name:      "match em coluna Name com whitespace e aceitavel",
			output:    "Foxit Reader    Foxit.FoxitReader    12.1.0\n",
			packageID: "Foxit",
			want:      true,
		},
		{
			name:      "match exato 7zip.7zip",
			output:    wingetListOutput,
			packageID: "7zip.7zip",
			want:      true,
		},
		{
			name:      "nao deve match em Microsoft quando ID e Microsoft.AppInstaller",
			output:    wingetListOutput,
			packageID: "Microsoft",
			want:      false,
		},
		{
			name:      "match Microsoft.AppInstaller completo",
			output:    wingetListOutput,
			packageID: "Microsoft.AppInstaller",
			want:      true,
		},
		{
			name:      "output vazio retorna false",
			output:    "",
			packageID: "Foxit.FoxitReader",
			want:      false,
		},
		{
			name:      "packageID vazio retorna false",
			output:    wingetListOutput,
			packageID: "",
			want:      false,
		},
		{
			name:      "ID no inicio da linha",
			output:    "Foxit.FoxitReader 12.1.0\n",
			packageID: "Foxit.FoxitReader",
			want:      true,
		},
		{
			name:      "ID no fim da linha",
			output:    "header\nFoxit.FoxitReader",
			packageID: "Foxit.FoxitReader",
			want:      true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isPackageInOutput(tc.output, tc.packageID)
			if got != tc.want {
				t.Errorf("isPackageInOutput(packageID=%q) = %v, want %v", tc.packageID, got, tc.want)
			}
		})
	}
}
