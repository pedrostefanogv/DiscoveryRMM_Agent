package automation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"discovery/app/core/processutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// Execução de sensores do tipo Script.
//
// O sensor de script é o único caso do motor que ABRE PROCESSO. As diferenças
// deliberadas em relação ao executor da automação (executeProcessWithEnv):
//
//   - stdout e stderr são capturados SEPARADOS (bytes.Buffer): o parser lê só o
//     stdout e o stderr vai como evidência para o servidor — misturar os dois
//     contaminaria a extração de canais (warnings de PowerShell no meio do
//     JSON do script);
//   - NÃO chama processutil.ApplyUserContext: o sensor roda como a identidade
//     do AGENTE (SYSTEM). Assumir o token do usuário logado quebraria
//     justamente os sensores de máquina (serviço, processo, disco);
//   - exit code != 0 NÃO é erro de coleta: quem decide é o servidor, aplicando
//     o mapa de exit codes do sensor (SensorExitRules no API).
//
// OptionsJson do sensor Script (contrato do servidor + plano):
//
//   - "scriptSource": "catalog" | "inline" (vazio = auto: catálogo quando o
//     sync trouxe o conteúdo, senão inline);
//   - "inlineContent": corpo do script inline (aliases antigos: script,
//     command, content, inlineScript);
//   - "inlineShell": shell/tipo do inline — PowerShell, cmd/Batch, Shell,
//     Python (aliases antigos: scriptType, type);
//   - "arguments": lista de argumentos anexados à execução;
//   - "envVars": lista "CHAVE=valor" adicionada ao ambiente do processo.
// ─────────────────────────────────────────────────────────────────────────────

// sensorProcessResult é o produto de uma execução de script de sensor.
type sensorProcessResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	// Err é falha de EXECUÇÃO (binário ausente, timeout): vira Error na amostra
	// (servidor marca Unknown + RunError). Exit code diferente de zero NÃO entra
	// aqui — é evidência, não erro.
	Err string
}

// executeSensorProcess roda o processo capturando stdout/stderr separados.
func executeSensorProcess(parent context.Context, executable string, args []string, timeout time.Duration, extraEnv []string) sensorProcessResult {
	if strings.TrimSpace(executable) == "" {
		return sensorProcessResult{Err: "executavel do sensor nao informado"}
	}
	if timeout <= 0 || timeout > sensorMaxTimeout {
		timeout = sensorMaxTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, executable, args...)
	processutil.HideWindow(cmd)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}

	runErr := cmd.Run()
	result := sensorProcessResult{
		Stdout: truncateOutput(stdout.String()),
		Stderr: truncateOutput(stderr.String()),
	}
	if ctx.Err() == context.DeadlineExceeded {
		// Timeout: evidência explícita (o servidor trata como Failing, com
		// severidade Critical), não como erro de coleta.
		result.TimedOut = true
		return result
	}
	if runErr == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result
	}
	result.ExitCode = 1
	result.Err = runErr.Error()
	return result
}

// ── Resolução do script (catálogo x inline) ──

// sensorScriptSource resolve QUAL script executar e de onde ele veio.
//
//	scriptType   — tipo efetivo (PowerShell/Batch/Shell/Python/Custom)
//	content      — corpo do script
//	fromCatalog  — true quando o corpo veio do catálogo sincronizado (só nesse
//	               caso o hash de integridade do servidor se aplica)
func sensorScriptSource(cfg *SensorConfig) (scriptType AutomationScriptType, content string, fromCatalog bool, err error) {
	source := strings.ToLower(strings.TrimSpace(cfg.Options.String("scriptSource", "source")))
	catalogAvailable := cfg.Policy.Script != nil && strings.TrimSpace(cfg.Policy.Script.Content) != ""

	useCatalog := false
	switch source {
	case "", "auto":
		useCatalog = catalogAvailable
	case "catalog":
		if !catalogAvailable {
			return "", "", false, fmt.Errorf("sensor de script com scriptSource=catalog, mas o script do catalogo nao foi sincronizado (conteudo ausente no sync)")
		}
		useCatalog = true
	case "inline":
		useCatalog = false
	default:
		// Valor desconhecido (servidor mais novo): mantém o comportamento
		// automático em vez de derrubar a coleta.
		useCatalog = catalogAvailable
	}

	if useCatalog {
		scriptType = cfg.Policy.Script.ScriptType
		if scriptType == "" {
			scriptType = ScriptPowerShell
		}
		return scriptType, cfg.Policy.Script.Content, true, nil
	}

	scriptType, content, err = sensorInlineScript(cfg)
	if err != nil {
		return "", "", false, err
	}
	return scriptType, content, false, nil
}

// sensorInlineScript lê o script inline do OptionsJson.
//
// "inlineShell" é o TIPO/shell do script; por compatibilidade com os primeiros
// sensores, se ele não for um nome de shell conhecido e não houver
// inlineContent, o valor é tratado como o próprio CORPO (com PowerShell).
func sensorInlineScript(cfg *SensorConfig) (AutomationScriptType, string, error) {
	content := strings.TrimSpace(cfg.Options.String("inlineContent", "inlineScript", "script", "command", "content"))
	shellRaw := strings.TrimSpace(cfg.Options.String("inlineShell", "scriptType", "type"))
	scriptType := sensorScriptShellType(shellRaw)

	if content == "" && shellRaw != "" && scriptType == "" {
		content = shellRaw
		scriptType = ScriptPowerShell
	}
	if content == "" {
		return "", "", fmt.Errorf("sensor de script sem conteudo (script do catalogo nao sincronizado e nenhum inlineContent nas opcoes)")
	}
	if scriptType == "" {
		// Tipo ausente/desconhecido: PowerShell é o shell padrão do produto.
		scriptType = ScriptPowerShell
	}
	return scriptType, content, nil
}

// sensorScriptShellType normaliza o nome do shell vindo do OptionsJson.
// Devolve "" quando não reconhece (o chamador decide o default).
func sensorScriptShellType(raw string) AutomationScriptType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "powershell", "ps", "ps1", "pwsh":
		return ScriptPowerShell
	case "cmd", "batch", "bat", "dos":
		return ScriptBatch
	case "shell", "sh", "bash", "zsh":
		return ScriptShell
	case "python", "python3", "py":
		return ScriptPython
	case "custom":
		return ScriptCustom
	}
	return ""
}

// ── Argumentos do script ──

// powerShellParameterPattern reconhece parâmetros/switch do PowerShell
// ("-Verbose", "-TimeoutSec"), que NÃO podem ir entre aspas (vira string literal
// e não liga no param()).
var powerShellParameterPattern = regexp.MustCompile("^-[A-Za-z][A-Za-z0-9_]*$")

// escapePowerShellArguments prepara os argumentos para serem anexados ao texto
// do -Command. Argumento literal vai entre aspas simples (forma literal do
// PowerShell, com aspas internas duplicadas); parâmetro nomeado fica cru.
func escapePowerShellArguments(arguments []string) []string {
	out := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		trimmed := strings.TrimSpace(argument)
		if trimmed == "" {
			continue
		}
		if powerShellParameterPattern.MatchString(trimmed) {
			out = append(out, trimmed)
			continue
		}
		out = append(out, "'"+strings.ReplaceAll(trimmed, "'", "''")+"'")
	}
	return out
}

// escapeCommandLineArguments prepara os argumentos anexados ao "cmd /C".
// O cmd não tem escaping real: argumento com espaço/vazio/metacaractere vai
// entre aspas duplas (aspas internas duplicadas). Limitação conhecida e aceita:
// argumento que mistura aspas duplas E metacaractere é caso raro.
func escapeCommandLineArguments(arguments []string) []string {
	out := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		trimmed := strings.TrimSpace(argument)
		if trimmed == "" {
			continue
		}
		if !strings.ContainsAny(trimmed, " \t\"&|<>^()") {
			out = append(out, trimmed)
			continue
		}
		out = append(out, "\""+strings.ReplaceAll(trimmed, "\"", "\"\"")+"\"")
	}
	return out
}

// sensorScriptEnvVars normaliza envVars ("CHAVE=valor"). Entrada sem "=" ou com
// chave vazia é IGNORADA: uma opção malformada no console não pode impedir o
// script inteiro de rodar (o servidor valida o formulário na UI).
func sensorScriptEnvVars(cfg *SensorConfig) []string {
	raw := cfg.Options.Strings("envVars", "env", "environment")
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		key, _, found := strings.Cut(entry, "=")
		if !found || strings.TrimSpace(key) == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// sensorScriptCommand monta executável + argumentos por tipo de script.
//
// Os "arguments" do OptionsJson são ANEXADOS ao comando: aqui o script chega ao
// shell como TEXTO (-Command, /C ou -c), então não existe argv separado para o
// conteúdo — anexar ao texto é a única forma de entregá-los:
//   - PowerShell: -Command "<content> <arg1> <arg2>" (arg escapado; sem
//     argumentos o texto é byte-a-byte o conteúdo, que é o caso comum);
//   - cmd (/C): "<content> <arg1> <arg2>" com quoting defensivo;
//   - Python: prefixo do interpretador RESOLVIDO + "-c <content>" + argv reais
//     (não há shell no meio: os argumentos vão como elementos de argv mesmo).
//
// O último retorno (resolvedInterpreter) só é significativo para Python: false
// significa que nenhum candidato passou na prova e o fallback "python" foi usado.
func sensorScriptCommand(scriptType AutomationScriptType, content string, arguments []string) (string, []string, bool, error) {
	switch scriptType {
	case ScriptPowerShell:
		command := content
		if len(arguments) > 0 {
			command = content + " " + strings.Join(escapePowerShellArguments(arguments), " ")
		}
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", command}, true, nil
	case ScriptBatch, ScriptShell, ScriptCustom:
		command := content
		if len(arguments) > 0 {
			command = content + " " + strings.Join(escapeCommandLineArguments(arguments), " ")
		}
		return "cmd", []string{"/C", command}, true, nil
	case ScriptPython:
		// Reusa a resolução do executor (py -3, python3, caminhos comuns, stub da
		// Microsoft Store rejeitado). "python" fixo quebrava em máquina onde só
		// existe o launcher.
		interpreter, resolved := resolvePythonInterpreter()
		args := pythonCommandArgs(interpreter, content)
		args = append(args, arguments...)
		return interpreter.name, args, resolved, nil
	default:
		// Tipo desconhecido (servidor mais novo): cai em PowerShell, o shell
		// padrão dos scripts do produto.
		command := content
		if len(arguments) > 0 {
			command = content + " " + strings.Join(escapePowerShellArguments(arguments), " ")
		}
		return "powershell", []string{"-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", command}, true, nil
	}
}

// verifySensorScriptIntegrity confere o hash declarado pelo servidor (mesmo
// algoritmo da automação: SHA256 do UTF-8 do conteúdo cru, hex lower).
func verifySensorScriptIntegrity(script *SensorScriptRef) error {
	if script == nil {
		return nil
	}
	expected := strings.ToLower(strings.TrimSpace(script.ContentHashSHA256))
	if expected == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(script.Content))
	actual := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("integridade do script do sensor falhou (sha256 divergente): esperado=%s... obtido=%s...", truncateHash(expected), truncateHash(actual))
	}
	return nil
}

// runSensorScript coleta um sensor do tipo Script.
func runSensorScript(ctx context.Context, cfg *SensorConfig) sensorCollection {
	scriptType, content, fromCatalog, err := sensorScriptSource(cfg)
	if err != nil {
		return sensorCollection{Err: err.Error()}
	}
	// Integridade só se aplica ao script do CATÁLOGO (o inline não passa pelo
	// pipeline de conteúdo do servidor).
	if fromCatalog {
		if err := verifySensorScriptIntegrity(cfg.Policy.Script); err != nil {
			return sensorCollection{Err: err.Error()}
		}
	}

	arguments := cfg.Options.Strings("arguments", "args")
	envVars := sensorScriptEnvVars(cfg)

	executable, args, resolvedInterpreter, err := sensorScriptCommand(scriptType, content, arguments)
	if err != nil {
		return sensorCollection{Err: err.Error()}
	}

	process := executeSensorProcess(ctx, executable, args, cfg.Timeout, envVars)
	parsed := parseSensorStdout(process.Stdout, cfg.DeclaredChannels, cfg.Extractors)

	measure := SensorMeasure{TimedOut: process.TimedOut}
	// Timeout não tem exit code confiável (o processo foi morto): o servidor
	// avalia TimedOut antes de qualquer código.
	if !process.TimedOut {
		measure.ExitCode = sensorInt(process.ExitCode)
	}
	if strings.TrimSpace(parsed.Output) != "" {
		measure.Stdout = sensorString(parsed.Output)
	}
	if strings.TrimSpace(process.Stderr) != "" {
		measure.Stderr = sensorString(process.Stderr)
	}

	collection := sensorCollection{
		Measure:         measure,
		Channels:        parsed.Channels,
		DeclaredStatus:  parsed.Status,
		DeclaredMessage: parsed.Message,
	}
	if process.Err != "" {
		collection.Err = process.Err
		// Máquina sem Python válido: mesma dica do executor, para o operador
		// saber que o problema é o interpretador e não o script.
		if scriptType == ScriptPython && !resolvedInterpreter {
			collection.Err = pythonMissingInterpreterHint + ": " + process.Err
		}
	}
	return collection
}
