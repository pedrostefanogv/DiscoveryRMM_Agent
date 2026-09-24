package inventory

import (
	"os"
	"path/filepath"
	"strings"
)

// chocolateyToolsRoot é o diretório padrão onde o Chocolatey instala pacotes
// "portable"/zip (ex.: C:	oolsdart-sdk). As ferramentas dentro dele costumam
// ser adicionadas ao PATH do USUÁRIO que instalou, não ao PATH da máquina.
const chocolateyToolsRoot = `C:\tools`

// packageManagerEnv devolve o ambiente do processo com o PATH enriquecido com os
// diretórios de ferramentas do Chocolatey que só existem no PATH do usuário
// interativo (ex.: C:	oolsdart-sdkin, adicionado pelo pacote dart-sdk).
//
// O serviço do Discovery roda como LocalSystem e herda apenas o PATH da máquina.
// Sem este enriquecimento, scripts de pós-instalação que invocam ferramentas por
// nome falham quando o update é disparado pelo RMM — mesmo funcionando no
// terminal do usuário. Foi exatamente o caso do upgrade do fvm, cujo
// chocolateyInstall.ps1 chama "dart pub get"/"dart compile exe".
func packageManagerEnv() []string {
	env := os.Environ()
	for i, entry := range env {
		// Variáveis de ambiente no Windows não diferenciam maiúsculas/minúsculas.
		if len(entry) >= 5 && strings.EqualFold(entry[:5], "PATH=") {
			env[i] = "PATH=" + enrichPathWith(entry[5:], chocolateyToolBinDirs())
			return env
		}
	}

	// PATH ausente (improvável): acrescenta um com os diretórios conhecidos.
	return append(env, "PATH="+enrichPathWith("", chocolateyToolBinDirs()))
}

// enrichPathWith concatena os diretórios extras ao PATH atual, removendo
// duplicatas e entradas vazias, preservando a ordem (PATH atual primeiro).
func enrichPathWith(current string, extras []string) string {
	seen := make(map[string]struct{})
	dirs := make([]string, 0, len(extras)+4)
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		key := strings.ToLower(strings.TrimRight(dir, `\/`))
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		dirs = append(dirs, dir)
	}

	for _, dir := range strings.Split(current, ";") {
		add(dir)
	}
	for _, dir := range extras {
		add(dir)
	}
	return strings.Join(dirs, ";")
}

// chocolateyToolBinDirs lista os diretórios "C:	ools<pacote>in" existentes.
func chocolateyToolBinDirs() []string {
	matches, err := filepath.Glob(filepath.Join(chocolateyToolsRoot, "*", "bin"))
	if err != nil {
		return nil
	}
	dirs := make([]string, 0, len(matches))
	for _, match := range matches {
		if info, statErr := os.Stat(match); statErr == nil && info.IsDir() {
			dirs = append(dirs, match)
		}
	}
	return dirs
}
