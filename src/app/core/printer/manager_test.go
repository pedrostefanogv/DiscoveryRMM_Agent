package printer

import (
	"strings"
	"testing"
)

func TestBuildInstallScriptValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name       string
		printer    string
		driverName string
		portName   string
		wantErr    string
	}{
		{name: "sem nome", printer: "", driverName: "Driver", portName: "IP_10.0.0.10", wantErr: "nome da impressora"},
		{name: "sem driver", printer: "HP", driverName: "", portName: "IP_10.0.0.10", wantErr: "driverName"},
		{name: "sem porta", printer: "HP", driverName: "Driver", portName: "", wantErr: "portName"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildInstallScript(tt.printer, tt.driverName, tt.portName, "")
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("erro esperado contendo %q, recebido: %v", tt.wantErr, err)
			}
		})
	}
}

func TestBuildInstallScriptEscapesUserInput(t *testing.T) {
	script, err := buildInstallScript("Fila d'Impressao", "Driver X", "PORTA'01", "10.0.0.15")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if !strings.Contains(script, "'Fila d''Impressao'") {
		t.Fatalf("script nao escapou o nome da impressora corretamente: %s", script)
	}
	if !strings.Contains(script, "'PORTA''01'") {
		t.Fatalf("script nao escapou a porta corretamente: %s", script)
	}
	if !strings.Contains(script, "Add-Printer -Name $printerName -DriverName $driverName -PortName $portName") {
		t.Fatalf("script nao contem o comando esperado de instalacao: %s", script)
	}
}

func TestBuildSharedInstallScriptValidatesUNCPath(t *testing.T) {
	invalidPaths := []string{"", "servidor\\fila", "\\servidor", "\\servidor\\", "\\servidor\\fila\\extra"}

	for _, path := range invalidPaths {
		_, err := buildSharedInstallScript(path, false)
		if err == nil || !strings.Contains(err.Error(), "connectionPath") {
			t.Fatalf("esperava erro de validacao para %q, recebido: %v", path, err)
		}
	}
}

func TestBuildSharedInstallScriptBuildsUNCInstall(t *testing.T) {
	script, err := buildSharedInstallScript(`\\srv-print\Financeiro`, true)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}

	if !strings.Contains(script, "Add-Printer -ConnectionName $connectionPath") {
		t.Fatalf("script nao contem a instalacao por ConnectionName: %s", script)
	}
	if !strings.Contains(script, "$setDefault = $true") {
		t.Fatalf("script nao propagou setDefault=true: %s", script)
	}
	if !strings.Contains(script, `'\\srv-print\Financeiro'`) {
		t.Fatalf("script nao contem o caminho UNC esperado: %s", script)
	}
	if !strings.Contains(script, `'Financeiro'`) {
		t.Fatalf("script nao extraiu o nome do share: %s", script)
	}
	if !strings.Contains(script, `Set-Printer -IsDefault $true`) {
		t.Fatalf("script nao contem configuracao de impressora padrao: %s", script)
	}
}
