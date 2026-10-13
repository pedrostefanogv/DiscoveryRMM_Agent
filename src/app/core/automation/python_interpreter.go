package automation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"discovery/app/core/processutil"
)

// Resolução do interpretador Python para scripts de automação.
//
// Problema real (agente 1.2.1): o executor chamava "python" fixo. Em máquinas
// Windows onde o único "python" do PATH é o App Execution Alias da Microsoft
// Store (um stub que imprime "Python was not found" e sai != 0) ou onde o Python
// está instalado só com o launcher "py"/num diretório fora do PATH, TODA tarefa
// RunScript de Python falhava na largada — o coletor era descartado e o circuit
// breaker pausava a task por 24h.
//
// A cadeia de fallback abaixo resolve na ordem:
//   1. MDZ_PYTHON (override do operador: só o executável, sem argumentos);
//   2. python  (o que o agente sempre usou: se funciona, o interpretador não muda);
//   3. python3;
//   4. py -3   (launcher oficial; fica em C:\Windows\py.exe, presente no PATH do SYSTEM
//      mesmo quando python.exe não está);
//   5. caminhos absolutos de instalações comuns (Program Files, Program Files (x86),
//      %LocalAppData%\Programs\Python\Python3*, C:\Python3*).
//
// Cada candidato é PROVADO executando "--version": o stub da Store é rejeitado
// (path em ...\WindowsApps\ e/ou saída "was not found") e só Python 3 é aceito.
// Sem candidato válido a resolução devolve "python" (comportamento antigo). O
// resultado — inclusive a falha — fica em cache por pythonResolutionTTL, para
// não repetir a varredura a cada execução e ainda assim pegar um Python
// instalado no meio da sessão sem reiniciar o agente.

// pythonProbeTimeout limita a prova de cada candidato (--version). Curto para não
// atrasar a execução quando a máquina não tem Python nenhum.
const pythonProbeTimeout = 4 * time.Second

// pythonInterpreter é o comando resolvido: name + argumentos que precedem o "-c".
type pythonInterpreter struct {
	name string
	args []string
}

// pythonDetector agrupa as dependências do sistema para o resolver ser testável
// sem tocar no ambiente real (LookPath/Glob/Stat/probe/GOOS injetáveis).
type pythonDetector struct {
	goos     string
	env      func(string) string
	lookPath func(string) (string, error)
	glob     func(string) ([]string, error)
	stat     func(string) (os.FileInfo, error)
	probe    func(pythonInterpreter) bool
}

var defaultPythonDetector = pythonDetector{
	goos:     runtime.GOOS,
	env:      os.Getenv,
	lookPath: exec.LookPath,
	glob:     filepath.Glob,
	stat:     os.Stat,
	probe:    probePythonInterpreter,
}

// pythonResolutionTTL: por quanto tempo uma resolução (inclusive FALHA) é
// reaproveitada. Sem TTL, máquina sem Python refazia a cadeia inteira a cada
// execução Python; com TTL curto, instalar o Python no meio da sessão passa a
// valer em poucos minutos.
const pythonResolutionTTL = 5 * time.Minute

type pythonResolution struct {
	interpreter pythonInterpreter
	resolved    bool
	at          time.Time
}

var (
	pythonMu         sync.Mutex
	cachedResolution *pythonResolution
)

// pythonMissingInterpreterHint vai para o ErrorMessage quando nenhum candidato
// passou na prova. Sem isso o agent só reportava "exit status <n>" e o operador
// não tinha como saber que a causa era o interpretador (foi o caso real do stub
// da Microsoft Store).
const pythonMissingInterpreterHint = "nenhum interpretador Python valido encontrado (tentei py -3, python3, python e instalacoes comuns). Instale o Python ou defina MDZ_PYTHON com o caminho do python.exe"

// resolvePythonInterpreter devolve o interpretador Python a usar e se ele foi
// resolvido de fato. A resolução é cacheada por pythonResolutionTTL (sucesso e
// falha) para não varrer a cadeia a cada execução.
func resolvePythonInterpreter() (pythonInterpreter, bool) {
	pythonMu.Lock()
	defer pythonMu.Unlock()

	if cached := cachedResolution; cached != nil && time.Since(cached.at) < pythonResolutionTTL {
		return cached.interpreter, cached.resolved
	}

	interpreter, resolved := detectPythonInterpreter(defaultPythonDetector)
	cachedResolution = &pythonResolution{interpreter: interpreter, resolved: resolved, at: time.Now()}
	return interpreter, resolved
}

// resetPythonInterpreterCache existe para os testes; em produção o cache é do
// processo com TTL.
func resetPythonInterpreterCache() {
	pythonMu.Lock()
	cachedResolution = nil
	pythonMu.Unlock()
}

// pythonCommandArgs monta os argumentos finais do script: prefixo do candidato
// (ex.: py -3) + "-c" + conteúdo.
func pythonCommandArgs(interpreter pythonInterpreter, content string) []string {
	args := make([]string, 0, len(interpreter.args)+2)
	args = append(args, interpreter.args...)
	args = append(args, "-c", content)
	return args
}

// detectPythonInterpreter percorre os candidatos. Devolve resolved=false quando
// nenhum passou na prova (chamador usa o fallback "python").
func detectPythonInterpreter(d pythonDetector) (pythonInterpreter, bool) {
	for _, candidate := range pythonCandidates(d) {
		if strings.TrimSpace(candidate.name) == "" {
			continue
		}

		if isAbsolutePythonPath(candidate.name) {
			if d.stat != nil {
				info, err := d.stat(candidate.name)
				if err != nil || info.IsDir() {
					continue
				}
			}
		} else {
			path, err := d.lookPath(candidate.name)
			if err != nil || strings.TrimSpace(path) == "" {
				continue
			}
			if isWindowsStoreAliasPath(path) {
				continue
			}
		}

		if d.probe(candidate) {
			return candidate, true
		}
	}

	return pythonInterpreter{name: "python"}, false
}

// pythonCandidates lista os candidatos na ordem de preferência.
func pythonCandidates(d pythonDetector) []pythonInterpreter {
	candidates := make([]pythonInterpreter, 0, 8)

	if override := strings.TrimSpace(d.env("MDZ_PYTHON")); override != "" {
		candidates = append(candidates, pythonInterpreter{name: override})
	}

	// "python" primeiro: é o interpretador que o agente sempre usou. Em máquina
	// onde ele executa, nada muda (scripts que dependem de pacotes instalados
	// naquele Python continuam iguais); o fallback só entra quando ele falha.
	candidates = append(candidates, pythonInterpreter{name: "python"})

	if d.goos != "windows" {
		return append(candidates, pythonInterpreter{name: "python3"})
	}

	candidates = append(candidates,
		pythonInterpreter{name: "python3"},
		// "-3" evita rodar Python 2 por engano; o launcher vive em C:\Windows e
		// está no PATH do SYSTEM mesmo quando o python.exe não está.
		pythonInterpreter{name: "py", args: []string{"-3"}},
	)

	return append(candidates, windowsPythonPathCandidates(d)...)
}

// windowsPythonPathCandidates procura instalações fora do PATH (mais recente
// primeiro: Python314 antes de Python312 antes de Python3.4).
func windowsPythonPathCandidates(d pythonDetector) []pythonInterpreter {
	if d.glob == nil {
		return nil
	}

	patterns := windowsPythonGlobPatterns(d)

	paths := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		matches, err := d.glob(pattern)
		if err != nil {
			continue
		}
		paths = append(paths, matches...)
	}

	// Mais recente primeiro pela VERSÃO do diretório (Python314 > Python312 >
	// Python3.4). Ordenação lexicográfica simples erraria: "Python34" > "Python314".
	sort.SliceStable(paths, func(i, j int) bool {
		iMajor, iMinor := pythonPathRank(paths[i])
		jMajor, jMinor := pythonPathRank(paths[j])
		if iMajor != jMajor {
			return iMajor > jMajor
		}
		if iMinor != jMinor {
			return iMinor > jMinor
		}
		return paths[i] > paths[j]
	})

	interpreterPaths := make([]pythonInterpreter, 0, len(paths))
	for _, path := range paths {
		interpreterPaths = append(interpreterPaths, pythonInterpreter{name: path})
	}
	return interpreterPaths
}

// windowsPythonGlobPatterns monta os padrões de busca de instalações fora do
// PATH. Todos precisam ser ABSOLUTOS: um padrão relativo casaria com o diretório
// de trabalho do agente.
func windowsPythonGlobPatterns(d pythonDetector) []string {
	patterns := make([]string, 0, 4)
	for _, root := range []string{d.env("ProgramFiles"), d.env("ProgramFiles(x86)")} {
		trimmed := strings.TrimSpace(root)
		if trimmed == "" {
			continue
		}
		patterns = append(patterns, filepath.Join(trimmed, "Python3*", "python.exe"))
	}
	// LocalAppData só entra quando existe: filepath.Join("") geraria um padrão
	// RELATIVO (Programs\Python\...) que poderia casar com o diretório de
	// trabalho do agente.
	if localAppData := strings.TrimSpace(d.env("LocalAppData")); localAppData != "" {
		patterns = append(patterns, filepath.Join(localAppData, "Programs", "Python", "Python3*", "python.exe"))
	}
	if drive := strings.TrimSpace(d.env("SystemDrive")); drive != "" {
		patterns = append(patterns, filepath.Join(drive+string(os.PathSeparator), "Python3*", "python.exe"))
	}

	return patterns
}

// isAbsolutePythonPath cobre também o caminho com drive do Windows quando o
// teste roda em outro SO (filepath.IsAbs depende do host).
func isAbsolutePythonPath(path string) bool {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "/") {
		return true
	}
	// Caminho com drive do Windows (filepath.IsAbs depende do host do teste).
	return len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

// pythonPathRank extrai (major, minor) do diretório da instalação
// (ex.: ...\Python314 -> 3.14; ...\Python3.4 -> 3.4).
// Sem versão reconhecível devolve (0, 0), que vai para o fim da lista.
func pythonPathRank(path string) (int, int) {
	base := filepath.Base(filepath.Dir(path))

	digits := make([]byte, 0, 4)
	for i := 0; i < len(base); i++ {
		ch := base[i]
		if ch >= '0' && ch <= '9' {
			digits = append(digits, ch)
			continue
		}
		if len(digits) > 0 {
			// "Python3.4" / "Python3_10" também valem: o separador não pode
			// truncar a versão em "3".
			if ch == '.' || ch == '_' {
				continue
			}
			break
		}
	}
	if len(digits) < 2 {
		return 0, 0
	}

	major := int(digits[0] - '0')
	minor, err := strconv.Atoi(string(digits[1:]))
	if err != nil {
		return 0, 0
	}
	return major, minor
}

// isWindowsStoreAliasPath identifica o App Execution Alias da Store
// (...\AppData\Local\Microsoft\WindowsApps\python.exe), que responde "Python was
// not found" e nunca executa código do usuário.
//
// Só o executável DIRETAMENTE em WindowsApps é o alias. Um Python da Store fica
// num subdiretório de pacote (...\WindowsApps\PythonSoftwareFoundation.Python.
// 3.14_...\python.exe) e continua sendo candidato normal (a prova decide).
func isWindowsStoreAliasPath(path string) bool {
	dir := strings.ToLower(filepath.ToSlash(filepath.Dir(path)))
	return dir == "windowsapps" || strings.HasSuffix(dir, "/windowsapps")
}

// probePythonInterpreter confirma que o candidato executa de verdade, sem abrir
// janela e com timeout curto.
func probePythonInterpreter(interpreter pythonInterpreter) bool {
	if strings.TrimSpace(interpreter.name) == "" {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), pythonProbeTimeout)
	defer cancel()

	args := append(append([]string{}, interpreter.args...), "--version")
	cmd := exec.CommandContext(ctx, interpreter.name, args...)
	processutil.HideWindow(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}

	text := strings.ToLower(string(output))
	// Defesa extra: se o stub da Store sair com 0 em alguma build, a mensagem
	// ainda o denuncia.
	if strings.Contains(text, "was not found") {
		return false
	}

	// Só Python 3: os scripts do produto são Python 3 (stdlib). Sem isso um
	// "python" 2.x no PATH venceria a cadeia e o script rodaria no interpretador
	// errado — e o "-3" do launcher nunca seria exercido.
	return isPython3VersionOutput(string(output))
}

// isPython3VersionOutput reconhece a saída de "--version" de um Python 3.x
// (ex.: "Python 3.14.7").
func isPython3VersionOutput(output string) bool {
	normalized := strings.ToLower(strings.TrimSpace(output))
	return strings.Contains(normalized, "python 3.") ||
		strings.Contains(normalized, "python 3 ") ||
		strings.HasSuffix(normalized, "python 3")
}
