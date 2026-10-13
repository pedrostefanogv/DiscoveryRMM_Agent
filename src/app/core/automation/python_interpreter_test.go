package automation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Cadeia de fallback do interpretador Python.
//
// Regressão do bug real: o executor usava "python" fixo; em máquina onde o
// único "python" do PATH é o App Execution Alias da Microsoft Store, TODA
// coleta/script Python falhava na largada e a task era pausada 24h pelo circuit
// breaker. A ordem preserva o comportamento antigo quando o "python" funciona e
// só cai para os fallbacks quando ele não executa.

type fakeFileInfo struct{ dir bool }

func (f fakeFileInfo) Name() string       { return "python.exe" }
func (f fakeFileInfo) Size() int64        { return 1 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

type fakePythonSystem struct {
	goos          string
	resolved      map[string]string   // LookPath: nome -> caminho
	absolute      map[string]bool     // caminhos absolutos "existentes"
	globs         map[string][]string // Glob: padrão -> matches
	env           map[string]string
	probeOK       map[string]bool // chave do candidato -> resultado da prova
	probed        []string
	lookPathCalls int
}

func candidateKey(candidate pythonInterpreter) string {
	if len(candidate.args) == 0 {
		return candidate.name
	}
	return candidate.name + " " + strings.Join(candidate.args, " ")
}

func (f *fakePythonSystem) detector() pythonDetector {
	env := func(key string) string { return f.env[key] }
	return pythonDetector{
		goos: f.goos,
		env:  env,
		lookPath: func(name string) (string, error) {
			f.lookPathCalls++
			if path, ok := f.resolved[name]; ok {
				return path, nil
			}
			return "", os.ErrNotExist
		},
		glob: func(pattern string) ([]string, error) {
			return f.globs[pattern], nil
		},
		stat: func(path string) (os.FileInfo, error) {
			if f.absolute[path] {
				return fakeFileInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
		probe: func(candidate pythonInterpreter) bool {
			key := candidateKey(candidate)
			f.probed = append(f.probed, key)
			return f.probeOK[key]
		},
	}
}

func TestDetectPythonInterpreter_PrefersWorkingPythonWhenAvailable(t *testing.T) {
	system := &fakePythonSystem{
		goos:     "windows",
		resolved: map[string]string{"python": "C:\\Program Files\\Python314\\python.exe", "py": "C:\\Windows\\py.exe"},
		probeOK:  map[string]bool{"python": true, "py -3": true},
	}

	interpreter, resolved := detectPythonInterpreter(system.detector())

	if !resolved {
		t.Fatal("esperava resolver o interpretador")
	}
	// Preserva o interpretador que o agente sempre usou quando ele executa.
	if interpreter.name != "python" || len(interpreter.args) != 0 {
		t.Fatalf("esperava python puro, obtive %#v", interpreter)
	}
}

func TestDetectPythonInterpreter_FallsBackToPyLauncherWhenPythonIsStoreStub(t *testing.T) {
	system := &fakePythonSystem{
		goos: "windows",
		resolved: map[string]string{
			"python":  "C:\\Users\\User\\AppData\\Local\\Microsoft\\WindowsApps\\python.exe",
			"python3": "C:\\Users\\User\\AppData\\Local\\Microsoft\\WindowsApps\\python3.exe",
			"py":      "C:\\Windows\\py.exe",
		},
		probeOK: map[string]bool{"py -3": true},
	}

	interpreter, resolved := detectPythonInterpreter(system.detector())

	if !resolved {
		t.Fatal("esperava resolver via launcher py")
	}
	if interpreter.name != "py" || len(interpreter.args) != 1 || interpreter.args[0] != "-3" {
		t.Fatalf("esperava py -3, obtive %#v", interpreter)
	}
}

func TestDetectPythonInterpreter_SkipsCandidateThatFailsProbe(t *testing.T) {
	system := &fakePythonSystem{
		goos:     "windows",
		resolved: map[string]string{"python": "C:\\Python27\\python.exe", "python3": "C:\\Program Files\\Python314\\python.exe"},
		probeOK:  map[string]bool{"python3": true},
	}

	interpreter, resolved := detectPythonInterpreter(system.detector())

	if !resolved || interpreter.name != "python3" {
		t.Fatalf("esperava cair para python3, obtive %#v (resolved=%v)", interpreter, resolved)
	}
}

func TestDetectPythonInterpreter_RejectsMicrosoftStoreAlias(t *testing.T) {
	system := &fakePythonSystem{
		goos: "windows",
		resolved: map[string]string{
			"python": "C:\\Users\\User\\AppData\\Local\\Microsoft\\WindowsApps\\python.exe",
		},
	}

	interpreter, resolved := detectPythonInterpreter(system.detector())

	if resolved {
		t.Fatalf("não deveria resolver só com o stub da Store: %#v", interpreter)
	}
	if interpreter.name != "python" {
		t.Fatalf("fallback deveria ser python (comportamento antigo), obtive %q", interpreter.name)
	}
	for _, probed := range system.probed {
		if probed == "python" {
			t.Fatal("o stub da Store não deve nem ser provado (executa o alias)")
		}
	}
}

func TestIsWindowsStoreAliasPath_OnlyRootAliasIsTheStub(t *testing.T) {
	alias := "C:\\Users\\User\\AppData\\Local\\Microsoft\\WindowsApps\\python.exe"
	storePython := "C:\\Program Files\\WindowsApps\\PythonSoftwareFoundation.Python.3.14_qbz5n2kfra8p0\\python.exe"

	if !isWindowsStoreAliasPath(alias) {
		t.Fatal("python.exe direto em WindowsApps é o alias e deve ser rejeitado")
	}
	if isWindowsStoreAliasPath(storePython) {
		t.Fatal("Python da Store em subdiretório de pacote deve ser candidato normal (a prova decide)")
	}
}

func TestDetectPythonInterpreter_FindsAbsoluteInstallWhenPathMissesIt(t *testing.T) {
	old313 := "C:\\Program Files\\Python313\\python.exe"
	new314 := "C:\\Program Files\\Python314\\python.exe"
	old34 := "C:\\Program Files\\Python34\\python.exe"
	system := &fakePythonSystem{
		goos: "windows",
		env: map[string]string{
			"ProgramFiles":      "C:\\Program Files",
			"ProgramFiles(x86)": "C:\\Program Files (x86)",
			"LocalAppData":      "C:\\Users\\User\\AppData\\Local",
			"SystemDrive":       "C:",
		},
		globs: map[string][]string{
			filepath.Join("C:\\Program Files", "Python3*", "python.exe"): {old34, new314, old313},
		},
		absolute: map[string]bool{old313: true, new314: true, old34: true},
		probeOK:  map[string]bool{new314: true, old313: true, old34: true},
	}

	interpreter, resolved := detectPythonInterpreter(system.detector())

	if !resolved {
		t.Fatal("esperava achar instalação absoluta")
	}
	if interpreter.name != new314 {
		t.Fatalf("esperava a instalação mais recente (%s), obtive %q", new314, interpreter.name)
	}
}

// Sem LocalAppData o padrão não pode virar relativo: filepath.Join("") produziria
// "Programs\Python\Python3*\python.exe", que casaria com o diretório de trabalho
// do agente.
func TestDetectPythonInterpreter_NoRelativeGlobWhenEnvMissing(t *testing.T) {
	system := &fakePythonSystem{
		goos: "windows",
		// Sem LocalAppData e sem SystemDrive: nada de padrão relativo.
		env: map[string]string{"ProgramFiles": "C:\\Program Files"},
	}

	patterns := windowsPythonGlobPatterns(system.detector())
	if len(patterns) == 0 {
		t.Fatal("esperava ao menos o padrão de Program Files")
	}
	for _, pattern := range patterns {
		if !isAbsolutePythonPath(strings.ReplaceAll(pattern, "*", "x")) {
			t.Fatalf("padrão deveria ser absoluto, obtive %q", pattern)
		}
	}
	for _, pattern := range patterns {
		if strings.Contains(strings.ToLower(pattern), "programs\\python") {
			t.Fatalf("LocalAppData ausente não pode gerar padrão relativo: %q", pattern)
		}
	}
}

func TestDetectPythonInterpreter_OverrideWins(t *testing.T) {
	custom := "/opt/custom/python"
	system := &fakePythonSystem{
		goos:     "linux",
		env:      map[string]string{"MDZ_PYTHON": custom},
		absolute: map[string]bool{custom: true},
		probeOK:  map[string]bool{custom: true},
	}

	interpreter, resolved := detectPythonInterpreter(system.detector())

	if !resolved || interpreter.name != custom {
		t.Fatalf("MDZ_PYTHON deveria vencer, obtive %#v (resolved=%v)", interpreter, resolved)
	}
}

func TestDetectPythonInterpreter_PreservesLegacyPythonOffWindows(t *testing.T) {
	system := &fakePythonSystem{
		goos:     "linux",
		resolved: map[string]string{"python3": "/usr/bin/python3", "python": "/usr/bin/python"},
		probeOK:  map[string]bool{"python": true, "python3": true},
	}

	interpreter, resolved := detectPythonInterpreter(system.detector())

	if !resolved || interpreter.name != "python" {
		t.Fatalf("fora do Windows deve manter python quando ele executa, obtive %#v (resolved=%v)", interpreter, resolved)
	}
}

func TestDetectPythonInterpreter_FallsBackToPython3OffWindows(t *testing.T) {
	system := &fakePythonSystem{
		goos:     "linux",
		resolved: map[string]string{"python3": "/usr/bin/python3"},
		probeOK:  map[string]bool{"python3": true},
	}

	interpreter, resolved := detectPythonInterpreter(system.detector())

	if !resolved || interpreter.name != "python3" {
		t.Fatalf("esperava python3, obtive %#v (resolved=%v)", interpreter, resolved)
	}
}

func TestPythonCommandArgs_KeepsLauncherPrefix(t *testing.T) {
	args := pythonCommandArgs(pythonInterpreter{name: "py", args: []string{"-3"}}, "print(1)")

	want := []string{"-3", "-c", "print(1)"}
	if len(args) != len(want) {
		t.Fatalf("esperava %v, obtive %v", want, args)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("esperava %v, obtive %v", want, args)
		}
	}
}

func TestPythonPathRank_Handles314Versus34(t *testing.T) {
	major314, minor314 := pythonPathRank("C:\\Program Files\\Python314\\python.exe")
	major34, minor34 := pythonPathRank("C:\\Program Files\\Python34\\python.exe")

	if major314 != 3 || minor314 != 14 {
		t.Fatalf("Python314 deveria ranquear 3.14, obtive %d.%d", major314, minor314)
	}
	if major34 != 3 || minor34 != 4 {
		t.Fatalf("Python34 deveria ranquear 3.4, obtive %d.%d", major34, minor34)
	}
	if minor314 <= minor34 {
		t.Fatal("Python314 deveria vir antes de Python34")
	}
}

func TestPythonPathRank_AcceptsDottedVersionDirectory(t *testing.T) {
	major, minor := pythonPathRank("C:\\Program Files\\Python3.4\\python.exe")
	if major != 3 || minor != 4 {
		t.Fatalf("Python3.4 deveria ranquear 3.4, obtive %d.%d", major, minor)
	}

	major, minor = pythonPathRank("C:\\Program Files\\Python3.10\\python.exe")
	if major != 3 || minor != 10 {
		t.Fatalf("Python3.10 deveria ranquear 3.10, obtive %d.%d", major, minor)
	}
}

func TestIsPython3VersionOutput(t *testing.T) {
	cases := map[string]bool{
		"Python 3.14.7": true,
		"Python 3.4.4":  true,
		"Python 3":      true,
		"Python 2.7.18": false,
		"":              false,
		"was not found": false,
	}

	for output, want := range cases {
		if got := isPython3VersionOutput(output); got != want {
			t.Fatalf("isPython3VersionOutput(%q) = %v, esperado %v", output, got, want)
		}
	}
}

// withFakeDetector troca o detector real pelo fake e restaura no fim.
func withFakeDetector(t *testing.T, system *fakePythonSystem) {
	t.Helper()

	original := defaultPythonDetector
	defaultPythonDetector = system.detector()
	resetPythonInterpreterCache()

	t.Cleanup(func() {
		defaultPythonDetector = original
		resetPythonInterpreterCache()
	})
}

func TestResolvePythonInterpreter_CachesSuccess(t *testing.T) {
	system := &fakePythonSystem{
		goos:     "windows",
		resolved: map[string]string{"python": "C:\\Program Files\\Python314\\python.exe"},
		probeOK:  map[string]bool{"python": true},
	}
	withFakeDetector(t, system)

	for i := 0; i < 3; i++ {
		interpreter, resolved := resolvePythonInterpreter()
		if !resolved || interpreter.name != "python" {
			t.Fatalf("esperava cache de sucesso, obtive %#v (resolved=%v)", interpreter, resolved)
		}
	}

	if len(system.probed) != 1 {
		t.Fatalf("a prova deveria rodar 1 vez (cache), rodou %d", len(system.probed))
	}
}

func TestResolvePythonInterpreter_CachesFailureAndExpires(t *testing.T) {
	system := &fakePythonSystem{goos: "windows"}
	withFakeDetector(t, system)

	if _, resolved := resolvePythonInterpreter(); resolved {
		t.Fatal("sem candidatos válidos não deveria resolver")
	}
	if _, resolved := resolvePythonInterpreter(); resolved {
		t.Fatal("segunda chamada também deveria continuar unresolved")
	}
	// 3 LookPath (python, python3, py) só na primeira chamada: a falha é cacheada.
	if system.lookPathCalls != 3 {
		t.Fatalf("falha deveria ser cacheada (3 LookPath), obtive %d", system.lookPathCalls)
	}

	// Expira o TTL: nova varredura.
	pythonMu.Lock()
	cachedResolution.at = time.Now().Add(-pythonResolutionTTL - time.Second)
	pythonMu.Unlock()

	if _, resolved := resolvePythonInterpreter(); resolved {
		t.Fatal("continua sem Python válido")
	}
	if system.lookPathCalls != 6 {
		t.Fatalf("após o TTL deveria varrer de novo (6 LookPath), obtive %d", system.lookPathCalls)
	}
}
