//go:build windows

package remotesession

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── cwd da sessão de terminal ──
//
// Fonte de verdade, em ordem de precedência:
//  1. cwd REPORTADO pelo shell via OSC 633 (shell_integration.go);
//  2. fallback heurístico: último cd/chdir/pushd/Set-Location/sl submetido em
//     term.in, resolvido e validado com os.Stat (cmd não tem hook de prompt e,
//     antes do 1º prompt, nenhum shell reportou nada);
//  3. cwd do PROCESSO do agent (comportamento antigo).
//
// Completar NUNCA pode falhar por cwd desconhecido: se nada for válido, o
// provider cai no diretório do processo (ou no "." do FS de completação).
//
// Limitações: o fallback só entende o comando quando ele é submetido pelo
// viewer (teclas), não scripts; caminhos POSIX reportados pelo WSL não são
// resolvíveis pelo filesystem Windows da completação.

const (
	// termCwdMaxInputLine limita o buffer da linha em digitação. Um paste sem
	// newline não pode crescer sem limite enquanto o usuário não aperta Enter.
	termCwdMaxInputLine = 4096
	// termCwdKeepInputTail é o quanto do tail é preservado quando o buffer
	// estoura (o começo da linha já não interessa para o cd).
	termCwdKeepInputTail = 1024
)

// termCwdState guarda o cwd reportado e o heurístico sob lock (o output chega
// na goroutine de leitura e o input do viewer na callback do NATS).
type termCwdState struct {
	mu         sync.RWMutex
	reported   string
	reportedAt time.Time
	heuristic  string
	inputLine  []byte
}

// reset zera o estado a cada novo console (uma sessão anterior não vale).
func (s *termCwdState) reset() {
	s.mu.Lock()
	s.reported = ""
	s.reportedAt = time.Time{}
	s.heuristic = ""
	s.inputLine = nil
	s.mu.Unlock()
}

// resetCwdState zera o cwd da sessão (chamado no Start do console).
func (st *SessionTerminal) resetCwdState() {
	st.cwd.reset()
}

// normalizeReportedCwd limpa o path vindo do OSC 633 (espaços e aspas em volta).
func normalizeReportedCwd(raw string) string {
	cwd := strings.TrimSpace(raw)
	if len(cwd) >= 2 {
		if (cwd[0] == '"' && cwd[len(cwd)-1] == '"') || (cwd[0] == '\'' && cwd[len(cwd)-1] == '\'') {
			cwd = strings.TrimSpace(cwd[1 : len(cwd)-1])
		}
	}
	return cwd
}

// applyReportedCwd atualiza o cwd reportado pelo shell e publica no viewer
// quando MUDOU (o prompt reporta a cada comando; repetir o mesmo path seria
// tráfego inútil — o term.ready do handshake carrega o valor corrente).
func (st *SessionTerminal) applyReportedCwd(raw string) {
	cwd := normalizeReportedCwd(raw)
	if cwd == "" {
		return
	}
	now := time.Now()
	st.cwd.mu.Lock()
	changed := cwd != st.cwd.reported
	st.cwd.reported = cwd
	st.cwd.reportedAt = now
	st.cwd.mu.Unlock()
	if changed {
		st.publishCwd(cwd, now)
	}
}

// ReportedCwd devolve o último cwd reportado pelo shell ("" se nenhum ainda).
func (st *SessionTerminal) ReportedCwd() string {
	st.cwd.mu.RLock()
	defer st.cwd.mu.RUnlock()
	return st.cwd.reported
}

// heuristicCwd devolve o cwd inferido do último cd/Set-Location submetido.
func (st *SessionTerminal) heuristicCwd() string {
	st.cwd.mu.RLock()
	defer st.cwd.mu.RUnlock()
	return st.cwd.heuristic
}

// CurrentCwd devolve o cwd EFETIVO da sessão para completação/ready. Só devolve
// um path que exista como diretório no host; senão cai para o próximo fallback
// (reportado inválido → heurístico inválido → cwd do processo).
func (st *SessionTerminal) CurrentCwd() string {
	if p := st.ReportedCwd(); isTermDir(p) {
		return p
	}
	if p := st.heuristicCwd(); isTermDir(p) {
		return p
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// completionWorkingDir é o cwd entregue aos providers de completação (e ao
// processo auxiliar do PowerShell). Nunca devolve erro.
func (st *SessionTerminal) completionWorkingDir() string {
	return st.CurrentCwd()
}

// termCwdPayload é o JSON do subject .term.cwd (contrato aditivo).
type termCwdPayload struct {
	Cwd       string `json:"cwd"`
	ShellKind string `json:"shellKind"`
	AtMs      int64  `json:"atMs"`
}

// publishCwd publica o cwd reportado no subject .term.cwd.
func (st *SessionTerminal) publishCwd(cwd string, at time.Time) {
	if st.natsStream == nil {
		return
	}
	st.mu.RLock()
	kind := st.termShellKind
	st.mu.RUnlock()
	payload := termCwdPayload{Cwd: cwd, ShellKind: kind, AtMs: at.UnixMilli()}
	if err := st.natsStream.PublishTermCwd(st.sessionID, payload); err != nil {
		st.notePublishError()
	}
}

// noteTerminalInput acumula o texto digitado e aplica o fallback de cd a cada
// linha COMPLETA (Enter). Linhas parciais ficam para o próximo frame.
func (st *SessionTerminal) noteTerminalInput(data string) {
	if data == "" {
		return
	}
	st.cwd.mu.Lock()
	buf := append(st.cwd.inputLine, data...)
	var lines []string
	start := 0
	for i, b := range buf {
		if b == '\r' || b == '\n' {
			if i > start {
				lines = append(lines, string(buf[start:i]))
			}
			start = i + 1
		}
	}
	rest := buf[start:]
	if len(rest) > termCwdMaxInputLine {
		rest = rest[len(rest)-termCwdKeepInputTail:]
	}
	st.cwd.inputLine = append(st.cwd.inputLine[:0], rest...)
	st.cwd.mu.Unlock()

	for _, line := range lines {
		st.applyCdLine(line)
	}
}

// applyCdLine aplica o fallback heurístico de cd/Set-Location. Só atualiza
// quando o alvo existe e é diretório (os.Stat): um cd para pasta inexistente
// não muda o cwd do shell e também não pode mudar o nosso.
func (st *SessionTerminal) applyCdLine(line string) {
	target, ok := parseCdCommand(line)
	if !ok {
		return
	}
	base := st.currentCwdForResolution()
	resolved, ok := resolveCdTarget(base, target)
	if !ok {
		return
	}
	st.cwd.mu.Lock()
	st.cwd.heuristic = resolved
	st.cwd.mu.Unlock()
}

// currentCwdForResolution devolve a base para resolver um cd relativo: o cwd
// reportado (mesmo que ainda não validado — é o que o shell pensa), senão o
// heurístico, senão o do processo.
func (st *SessionTerminal) currentCwdForResolution() string {
	st.cwd.mu.RLock()
	reported := st.cwd.reported
	heuristic := st.cwd.heuristic
	st.cwd.mu.RUnlock()
	if reported != "" {
		return reported
	}
	if heuristic != "" {
		return heuristic
	}
	wd, _ := os.Getwd()
	return wd
}

// parseCdCommand reconhece cd/chdir/pushd/Set-Location/sl e devolve o alvo
// ("" = home). ok=false quando a linha não é um comando de troca de diretório.
func parseCdCommand(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	name := line
	rest := ""
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		name, rest = line[:i], line[i:]
	}
	switch strings.ToLower(name) {
	case "cd", "chdir", "pushd", "set-location", "sl":
	default:
		return "", false
	}
	return cutCdArguments(rest), true
}

// cutCdArguments remove flags conhecidas (-Path, /d, ...), corta em separadores
// de comando (;, |, &) fora de aspas e desembrulha o path entre aspas.
func cutCdArguments(rest string) string {
	rest = strings.TrimSpace(rest)
	for {
		first := firstCdField(rest)
		if !isCdFlag(first) {
			break
		}
		rest = strings.TrimSpace(rest[len(first):])
	}
	rest = cutAtCommandSeparator(rest)
	rest = strings.TrimSpace(rest)
	if len(rest) >= 2 {
		q := rest[0]
		if (q == '"' || q == '\'') && rest[len(rest)-1] == q {
			inner := rest[1 : len(rest)-1]
			if q == '"' {
				inner = strings.ReplaceAll(inner, "\"\"", "\"")
			}
			rest = strings.TrimSpace(inner)
		}
	}
	return rest
}

// firstCdField devolve o primeiro token (até espaço/tab).
func firstCdField(s string) string {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// isCdFlag reconhece flags de cd/Set-Location que não são o alvo.
func isCdFlag(tok string) bool {
	switch strings.ToLower(tok) {
	case "/d", "-p", "-l", "-path", "-literalpath", "-passthru", "--":
		return true
	}
	return false
}

// cutAtCommandSeparator corta a string no primeiro ; | & fora de aspas.
func cutAtCommandSeparator(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == ';' || c == '|' || c == '&' {
			return s[:i]
		}
	}
	return s
}

// expandCdTilde expande ~, ~/ e ~barra para o home do usuário.
func expandCdTilde(p string) string {
	if p == "" {
		return p
	}
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, "~\\") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// resolveCdTarget resolve e VALIDA (os.Stat) o alvo do cd. Devolve ok=false
// quando o caminho não existe ou não é diretório — nesse caso o cwd não muda.
func resolveCdTarget(base, target string) (string, bool) {
	if strings.TrimSpace(target) == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		target = home
	}
	target = expandCdTilde(target)
	if !filepath.IsAbs(target) {
		if strings.TrimSpace(base) == "" {
			if wd, err := os.Getwd(); err == nil {
				base = wd
			}
		}
		if base == "" {
			return "", false
		}
		target = filepath.Join(base, target)
	}
	target = filepath.Clean(target)
	if !isTermDir(target) {
		return "", false
	}
	return target, true
}

// isTermDir informa se o path existe e é diretório.
func isTermDir(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// ── Completacão com o cwd da SESSÃO ──

// newSessionCompletionRegistry monta um registry cujos providers cmd/POSIX
// resolvem o diretório corrente pela função cwd (o cwd reportado da sessão) em
// vez do cwd do processo do agent. Os providers são os mesmos do registry
// padrão (completion_provider.go), apenas com o FS injetado.
func newSessionCompletionRegistry(cwd func() (string, error)) *completionRegistry {
	fs := defaultCompletionFS
	fs.Cwd = cwd
	return &completionRegistry{providers: []completionProvider{
		newPowerShellCompletionProvider(),
		newCmdCompletionProvider(fs),
		newPOSIXCompletionProvider(fs),
	}}
}

// completionCwdContextKey carrega o cwd da sessão até o provider PowerShell, que
// executa a consulta num processo auxiliar (o registry só repassa ctx).
type completionCwdContextKey struct{}

// withCompletionCwd anexa o cwd ao contexto ("" = mantém o cwd do processo).
func withCompletionCwd(ctx context.Context, cwd string) context.Context {
	if strings.TrimSpace(cwd) == "" {
		return ctx
	}
	return context.WithValue(ctx, completionCwdContextKey{}, cwd)
}

// completionCwdFromContext lê o cwd anexado ao contexto ("" se ausente).
func completionCwdFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(completionCwdContextKey{}).(string); ok {
		return v
	}
	return ""
}
