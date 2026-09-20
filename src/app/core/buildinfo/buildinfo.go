// Package buildinfo expõe a revisão de código embutida no binário (VCS
// stamping do toolchain Go) para assinar os logs do agente — todos os
// arquivos em ProgramData\Discovery\logs passam a declarar qual versão do
// código gerou cada linha.
package buildinfo

import (
	"runtime/debug"
	"sync"
)

var (
	once  sync.Once
	cache string
)

// Revision retorna o hash curto (8 chars) do commit que gerou o binário, com
// sufixo "+mod" quando o worktree estava modificado no build. Sem VCS
// stamping (cópia sem .git, -buildvcs=false, go run), retorna "unknown" — os
// logs continuam válidos, apenas sem a assinatura de versão.
//
// O stamping é embutido por `go build` (≥1.18) quando o binário é
// compilado a partir de um checkout git; o build oficial usa `wails build`
// (que chama go build com o default -buildvcs=auto) a partir do checkout.
func Revision() string {
	once.Do(func() {
		cache = "unknown"
		info, ok := debug.ReadBuildInfo()
		if !ok {
			return
		}
		rev := ""
		dirty := false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev == "" {
			return
		}
		short := rev
		if len(short) > 8 {
			short = short[:8]
		}
		if dirty {
			short += "+mod"
		}
		cache = short
	})
	return cache
}
