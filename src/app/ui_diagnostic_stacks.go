package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"discovery/app/core/platform"
)

// ── Forense de travamento: stacks de goroutines deste processo ─────────────
//
// O ui-watchdog do serviço detecta a UI travada (processo vivo sem IPC) e
// antes de encerrá-la pede as stacks (IPCMsgDiagnosticStacks) e grava um
// minidump (ui_watchdog_dump_windows.go). O runtime de Go responde pelo read
// loop do cliente IPC mesmo com a main thread presa no WebView2/Win32 — é o
// que torna o congelamento diagnosicável sem reproduzi-lo na mão.

// uiStacksKeep é quantos arquivos de stacks ficam retidos em logs/dumps.
const uiStacksKeep = 3

// writeRuntimeStacksDump grava as stacks de TODAS as goroutines em
// <LogDir>/dumps/ui-stacks-<pid>-<ts>.txt e devolve o caminho. Roda em
// goroutine própria (chamador é o read loop do IPC).
func (a *App) writeRuntimeStacksDump(reason string) string {
	if a == nil {
		return ""
	}
	dir := filepath.Join(platform.LogDir(), "dumps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		a.Logs.Append("[diag] falha ao criar diretorio de dumps: " + err.Error())
		return ""
	}
	path := filepath.Join(dir, fmt.Sprintf("ui-stacks-%d-%s.txt", os.Getpid(), time.Now().Format("20060102-150405")))

	stacks := allGoroutineStacks()
	if err := os.WriteFile(path, stacks, 0o644); err != nil {
		a.Logs.Append("[diag] falha ao gravar stacks (" + reason + "): " + err.Error())
		return ""
	}
	pruneOldestFiles(dir, "ui-stacks-", uiStacksKeep)
	a.Logs.Append("[diag] stacks do processo gravadas (" + reason + ", " +
		fmt.Sprintf("%d goroutines", strings.Count(string(stacks), "\n\ngoroutine ")) + "): " + path)
	return path
}

// allGoroutineStacks captura runtime.Stack(all=true) com crescimento duplo
// até caber (teto de 16 MiB: um travamento total não precisa de mais que isso).
func allGoroutineStacks() []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return buf[:n]
		}
		if len(buf) >= 16<<20 {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

// pruneOldestFiles mantém apenas os `keep` arquivos MAIS RECENTES que casam o
// prefixo no diretório. Falha é ignorada (housekeeping, não caminho crítico).
func pruneOldestFiles(dir, prefix string, keep int) {
	if keep <= 0 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var matches []os.FileInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		matches = append(matches, info)
	}
	if len(matches) <= keep {
		return
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ModTime().After(matches[j].ModTime())
	})
	for _, info := range matches[keep:] {
		_ = os.Remove(filepath.Join(dir, info.Name()))
	}
}

// dumpDir devolve o diretório de dumps forenses (criado sob demanda).
func dumpDir() string {
	return filepath.Join(platform.LogDir(), "dumps")
}
