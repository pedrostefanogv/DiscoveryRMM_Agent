//go:build windows

package automation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Testes do OptionsJson do sensor Script: scriptSource, inlineContent,
// inlineShell, arguments e envVars (contrato do servidor).
// ─────────────────────────────────────────────────────────────────────────────

func newSensorScriptConfig(optionsJSON string, script *SensorScriptRef) SensorConfig {
	policy := SensorPolicy{
		SensorID:    "22222222-2222-2222-2222-222222222222",
		Name:        "sensor de script de teste",
		SensorType:  SensorTypeScript,
		OptionsJSON: optionsJSON,
		Channels:    []SensorChannel{{Key: "valor", IsPrimary: true}},
		Script:      script,
	}
	return buildSensorConfig(policy, time.Minute)
}

func TestSensorScriptInlineResolution(t *testing.T) {
	cases := []struct {
		name        string
		optionsJSON string
		wantType    AutomationScriptType
		wantContent string
		wantErr     bool
	}{
		{
			name:        "inlineContent com tipo default",
			optionsJSON: `{"inlineContent":"Write-Output 1"}`,
			wantType:    ScriptPowerShell,
			wantContent: "Write-Output 1",
		},
		{
			name:        "inlineShell define o tipo",
			optionsJSON: `{"inlineShell":"python","inlineContent":"print(1)"}`,
			wantType:    ScriptPython,
			wantContent: "print(1)",
		},
		{
			name:        "inlineShell maiusculo tambem e aceito",
			optionsJSON: `{"inlineShell":"Batch","inlineContent":"dir"}`,
			wantType:    ScriptBatch,
			wantContent: "dir",
		},
		{
			name:        "inlineShell como corpo (compat)",
			optionsJSON: `{"inlineShell":"Write-Output 2"}`,
			wantType:    ScriptPowerShell,
			wantContent: "Write-Output 2",
		},
		{
			name:        "aliases antigos seguem valendo",
			optionsJSON: `{"scriptType":"Batch","script":"dir /b"}`,
			wantType:    ScriptBatch,
			wantContent: "dir /b",
		},
		{
			name:        "inlineContent vence alias content",
			optionsJSON: `{"inlineContent":"novo","content":"velho"}`,
			wantType:    ScriptPowerShell,
			wantContent: "novo",
		},
		{
			name:        "sem conteudo nenhum",
			optionsJSON: `{"inlineShell":"powershell"}`,
			wantErr:     true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := newSensorScriptConfig(testCase.optionsJSON, nil)
			scriptType, content, fromCatalog, err := sensorScriptSource(&cfg)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("esperava erro, veio type=%q content=%q", scriptType, content)
				}
				return
			}
			if err != nil {
				t.Fatalf("erro inesperado: %v", err)
			}
			if fromCatalog {
				t.Fatal("fromCatalog = true, want inline")
			}
			if scriptType != testCase.wantType {
				t.Fatalf("tipo = %q, want %q", scriptType, testCase.wantType)
			}
			if content != testCase.wantContent {
				t.Fatalf("conteudo = %q, want %q", content, testCase.wantContent)
			}
		})
	}
}

func TestSensorScriptSourceCatalogSelection(t *testing.T) {
	catalog := &SensorScriptRef{ScriptType: ScriptPowerShell, Content: "Write-Output catalogo"}

	t.Run("auto prefere o catalogo sincronizado", func(t *testing.T) {
		cfg := newSensorScriptConfig("", catalog)
		scriptType, content, fromCatalog, err := sensorScriptSource(&cfg)
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !fromCatalog {
			t.Fatal("fromCatalog = false, want true")
		}
		if scriptType != ScriptPowerShell || content != catalog.Content {
			t.Fatalf("type=%q content=%q", scriptType, content)
		}
	})

	t.Run("scriptSource inline ignora o catalogo", func(t *testing.T) {
		cfg := newSensorScriptConfig(`{"scriptSource":"inline","inlineContent":"Write-Output inline"}`, catalog)
		_, content, fromCatalog, err := sensorScriptSource(&cfg)
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if fromCatalog || content != "Write-Output inline" {
			t.Fatalf("fromCatalog=%t content=%q", fromCatalog, content)
		}
	})

	t.Run("scriptSource catalog sem script sincronizado", func(t *testing.T) {
		cfg := newSensorScriptConfig(`{"scriptSource":"catalog","inlineContent":"Write-Output x"}`, nil)
		if _, _, _, err := sensorScriptSource(&cfg); err == nil {
			t.Fatal("catalog sem conteudo deveria falhar com erro claro")
		} else if !strings.Contains(err.Error(), "catalog") {
			t.Fatalf("erro = %q, want mencionar catalog", err.Error())
		}
	})

	t.Run("runSensorScript propaga o erro sem abrir processo", func(t *testing.T) {
		cfg := newSensorScriptConfig(`{"scriptSource":"catalog"}`, nil)
		collection := runSensorScript(context.Background(), &cfg)
		if collection.Err == "" {
			t.Fatal("esperava erro de coleta")
		}
		if collection.Measure.ExitCode != nil {
			t.Fatalf("exitCode = %v, want nil (nenhum processo executado)", *collection.Measure.ExitCode)
		}
	})
}

func TestSensorScriptCommandArguments(t *testing.T) {
	t.Run("powershell sem argumentos mantem o conteudo intacto", func(t *testing.T) {
		executable, args, _, err := sensorScriptCommand(ScriptPowerShell, "Write-Output 1", nil)
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if executable != "powershell" {
			t.Fatalf("executavel = %q", executable)
		}
		if last := args[len(args)-1]; last != "Write-Output 1" {
			t.Fatalf("command = %q, want o conteudo cru", last)
		}
	})

	t.Run("powershell escapa literais e mantem parametros", func(t *testing.T) {
		_, args, _, err := sensorScriptCommand(ScriptPowerShell, "& { param($nome) Write-Output $nome }", []string{"maria silva", "-Verbose", "a'b"})
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		command := args[len(args)-1]
		if !strings.HasSuffix(command, "'maria silva' -Verbose 'a''b'") {
			t.Fatalf("command = %q", command)
		}
		if !strings.HasPrefix(command, "& { param($nome) Write-Output $nome } ") {
			t.Fatalf("command = %q, want o conteudo preservado", command)
		}
	})

	t.Run("cmd anexa os argumentos citando o que precisa", func(t *testing.T) {
		executable, args, _, err := sensorScriptCommand(ScriptBatch, "dir", []string{"a b", "simples"})
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if executable != "cmd" || len(args) != 2 {
			t.Fatalf("executavel=%q args=%v", executable, args)
		}
		if args[1] != "dir \"a b\" simples" {
			t.Fatalf("command = %q", args[1])
		}
	})

	t.Run("python usa o interpretador resolvido e argv real", func(t *testing.T) {
		executable, args, _, err := sensorScriptCommand(ScriptPython, "print(1)", []string{"-x", "valor"})
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if strings.TrimSpace(executable) == "" {
			t.Fatal("executavel vazio para Python")
		}
		if len(args) < 4 {
			t.Fatalf("args = %v, want prefixo + -c + conteudo + argumentos", args)
		}
		if args[len(args)-4] != "-c" || args[len(args)-3] != "print(1)" || args[len(args)-2] != "-x" || args[len(args)-1] != "valor" {
			t.Fatalf("args = %v", args)
		}
	})
}

func TestSensorScriptEnvVarsFiltering(t *testing.T) {
	cfg := newSensorScriptConfig(`{"envVars":["A=1","sem-igual","=vazio","B=2"]}`, nil)
	envVars := sensorScriptEnvVars(&cfg)
	if len(envVars) != 2 || envVars[0] != "A=1" || envVars[1] != "B=2" {
		t.Fatalf("envVars = %v, want [A=1 B=2]", envVars)
	}
}

// TestSensorScriptInlineArgumentsAndEnvVars roda PowerShell de verdade: proves
// que arguments e envVars chegam ao processo (e que o inline e executado).
func TestSensorScriptInlineArgumentsAndEnvVars(t *testing.T) {
	content := "& { param($nome) Write-Output ('arg=' + $nome + ' env=' + $env:MDZ_SENSOR_TESTE) }"
	optionsBytes, err := json.Marshal(map[string]any{
		"scriptSource":  "inline",
		"inlineShell":   "powershell",
		"inlineContent": content,
		"arguments":     []string{"maria"},
		"envVars":       []string{"MDZ_SENSOR_TESTE=ok"},
	})
	if err != nil {
		t.Fatalf("json do options: %v", err)
	}

	cfg := newSensorScriptConfig(string(optionsBytes), nil)
	collection := runSensorScript(context.Background(), &cfg)
	if collection.Err != "" {
		t.Fatalf("erro de coleta: %s (stderr=%v)", collection.Err, collection.Measure.Stderr)
	}
	if collection.Measure.ExitCode == nil || *collection.Measure.ExitCode != 0 {
		t.Fatalf("exitCode = %v, stderr = %v", collection.Measure.ExitCode, collection.Measure.Stderr)
	}
	if collection.Measure.Stdout == nil {
		t.Fatal("stdout ausente")
	}
	stdout := *collection.Measure.Stdout
	if !containsText(stdout, "arg=maria") {
		t.Fatalf("stdout = %q, want o argumento 'maria'", stdout)
	}
	if !containsText(stdout, "env=ok") {
		t.Fatalf("stdout = %q, want a envVar MDZ_SENSOR_TESTE=ok", stdout)
	}
}
