//go:build windows

package remotesession

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"discovery/app/core/terminal"
)

// ── Providers de completação (TAB) ──
//
// A completação deixou de ser exclusiva do PowerShell: cada família de shell
// (PowerShell/TabExpansion2, cmd.exe e POSIX: bash/zsh/WSL) é um
// completionProvider. O registry escolhe o provider por shellKind e backend
// (ConPTY) e é a ÚNICA fonte de verdade para completionSupported — inclusive
// para o campo term.ready.supportsCompletionQuery.
//
// Nada aqui executa comando no runspace do usuário: o provider PowerShell usa
// processo auxiliar (TabExpansion2) e os providers cmd/POSIX são resolução
// puramente local (FS + PATH), com operações de FS injetáveis.

// completionProvider é o contrato de um provedor de completação.
type completionProvider interface {
	// Supports informa se o provider atende o shell/backend em uso.
	Supports(shellKind terminal.ShellKind, backend string) bool
	// Complete devolve os itens de completação para o texto/cursor (cursor em
	// runas, relativo a input).
	Complete(ctx context.Context, input string, cursor int) ([]completionMatch, error)
}

// completionSpanProvider é implementado pelos providers que também informam o
// trecho a substituir (replacementIndex/replacementLength). O PowerShell precisa
// dele porque esses valores vêm do TabExpansion2 (e não de uma heurística de
// token) — mantém o comportamento anterior bit a bit.
type completionSpanProvider interface {
	CompleteSpan(ctx context.Context, input string, cursor int) (tabExpansionResult, error)
}

// errCompletionUnsupported indica shell/backend sem provider registrado.
var errCompletionUnsupported = errors.New("completion nao suportado pelo shell/backend em uso")

// completionRegistry mantém os providers na ordem de precedência.
type completionRegistry struct {
	providers []completionProvider
}

// newCompletionRegistry cria o registry padrão: PowerShell, cmd e POSIX.
func newCompletionRegistry() *completionRegistry {
	return &completionRegistry{providers: []completionProvider{
		newPowerShellCompletionProvider(),
		newCmdCompletionProvider(defaultCompletionFS),
		newPOSIXCompletionProvider(defaultCompletionFS),
	}}
}

// defaultCompletionRegistry é usado pela sessão (completionSupported/entry).
var defaultCompletionRegistry = newCompletionRegistry()

// providerFor devolve o primeiro provider que atende shell/backend (nil se
// nenhum).
func (r *completionRegistry) providerFor(shellKind terminal.ShellKind, backend string) completionProvider {
	if r == nil {
		return nil
	}
	for _, p := range r.providers {
		if p.Supports(shellKind, backend) {
			return p
		}
	}
	return nil
}

// Supported informa se existe provider para o par shell/backend.
func (r *completionRegistry) Supported(shellKind terminal.ShellKind, backend string) bool {
	return r.providerFor(shellKind, backend) != nil
}

// Complete resolve o provider, remove o prefixo de prompt da linha do viewer,
// delega a consulta e devolve o resultado já truncado em completionMaxItems.
//
// Erros do provider são propagados para o caller responder ok=false — a sessão
// NUNCA é encerrada por falha de completação.
func (r *completionRegistry) Complete(ctx context.Context, shellKind terminal.ShellKind, backend, input string, cursor int) (tabExpansionResult, error) {
	p := r.providerFor(shellKind, backend)
	if p == nil {
		return tabExpansionResult{}, errCompletionUnsupported
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, completionProcessTimeout)
		defer cancel()
	}

	// O viewer (ConPTY) envia a linha do xterm INCLUINDO o prompt. Remove o
	// prefixo antes de consultar e devolve o ReplacementIndex na coordenada da
	// linha ORIGINAL (o viewer aplica sobre ela).
	queryInput, queryCursor, prefixRunes := sanitizeCompletionInput(input, cursor)

	var (
		result tabExpansionResult
		err    error
	)
	if sp, ok := p.(completionSpanProvider); ok {
		result, err = sp.CompleteSpan(ctx, queryInput, queryCursor)
	} else {
		var matches []completionMatch
		matches, err = p.Complete(ctx, queryInput, queryCursor)
		begin := completionDefaultSpan(queryInput, queryCursor)
		result = tabExpansionResult{
			ReplacementIndex:  begin,
			ReplacementLength: queryCursor - begin,
			Matches:           matches,
		}
	}
	if err != nil {
		return tabExpansionResult{}, err
	}

	result.ReplacementIndex += prefixRunes
	if len(result.Matches) > completionMaxItems {
		result.Matches = result.Matches[:completionMaxItems]
	}
	if result.Matches == nil {
		result.Matches = []completionMatch{}
	}
	return result, nil
}

// ── Filesystem injetável ──

// completionFSEntry descreve uma entrada de diretório. Struct própria (e não
// os.DirEntry) para que os testes injetem um FS falso sem tocar o disco.
type completionFSEntry struct {
	Name  string
	IsDir bool
}

// completionFS agrupa TODAS as operações de filesystem usadas pelos providers
// cmd/POSIX. Os testes substituem os campos por funções determinísticas —
// nenhum teste depende do FS real do host.
type completionFS struct {
	ListDir  func(dir string) ([]completionFSEntry, error)
	PathDirs func() []string
	HomeDir  func() string
	Cwd      func() (string, error)
}

// defaultCompletionFS é a implementação real (host Windows).
var defaultCompletionFS = completionFS{
	ListDir:  osListCompletionDir,
	PathDirs: osCompletionPathDirs,
	HomeDir:  osCompletionHomeDir,
	Cwd:      os.Getwd,
}

// osListCompletionDir lê um diretório do host.
func osListCompletionDir(dir string) ([]completionFSEntry, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]completionFSEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, completionFSEntry{Name: e.Name(), IsDir: e.IsDir()})
	}
	return out, nil
}

// osCompletionPathDirs devolve os diretórios do PATH do processo.
func osCompletionPathDirs() []string {
	return filepath.SplitList(os.Getenv("PATH"))
}

// osCompletionHomeDir devolve o diretório home do usuário ("" se indisponível).
func osCompletionHomeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// ── Cache de executáveis do PATH ──

const (
	// completionPathTTL é o TTL do cache de executáveis do PATH. Sem ele, cada
	// TAB varreria todos os diretórios do PATH (dezenas de ReadDir).
	completionPathTTL = 30 * time.Second

	// completionPathMaxEntries limita quantos nomes o cache guarda, para não
	// crescer sem limite em PATHs gigantes. O corte é feito DEPOIS de deduplicar
	// e ordenar — ver scanPathExecutables.
	completionPathMaxEntries = 4000

	// completionPathDirTimeout limita a listagem de CADA diretório do PATH. Um
	// diretório UNC morto (\servidorshare) pode bloquear o ReadDir por
	// minutos; sem este teto ele segurava a varredura inteira e, por consequência,
	// todos os TABs concorrentes.
	completionPathDirTimeout = 500 * time.Millisecond

	// completionPathScanTimeout limita a varredura inteira (todos os diretórios).
	completionPathScanTimeout = 5 * time.Second

	// completionPathScanWorkers limita as listagens simultâneas, para não abrir
	// dezenas de handles de uma vez em PATHs grandes.
	completionPathScanWorkers = 8
)

// completionPathScan representa uma varredura em andamento (single-flight).
// Quem chega enquanto ela roda aguarda done em vez de disparar outra varredura.
type completionPathScan struct {
	done  chan struct{}
	names []string
}

// completionPathIndex cacheia os nomes de executáveis do PATH com TTL. A chave
// é o conjunto de diretórios: mudou o PATH (ou expirou o TTL), varre de novo.
//
// A varredura NUNCA acontece com mu travado: apenas a leitura/escrita do cache
// e a eleição do líder do single-flight ficam sob o mutex. Assim um diretório
// lento não serializa os TABs concorrentes.
type completionPathIndex struct {
	mu         sync.Mutex
	ttl        time.Duration
	now        func() time.Time
	dirTimeout time.Duration
	key        string
	names      []string
	fetched    time.Time
	inflight   *completionPathScan
	flightKey  string
}

// newCompletionPathIndex cria o cache com o TTL padrão.
func newCompletionPathIndex() *completionPathIndex {
	return &completionPathIndex{ttl: completionPathTTL, now: time.Now, dirTimeout: completionPathDirTimeout}
}

// get devolve os executáveis do PATH, varrendo o disco apenas quando o PATH
// muda ou o TTL expira.
//
// Single-flight: só um chamador por chave executa scan; os demais aguardam o
// resultado (ou desistem quando ctx é cancelado). A varredura roda em goroutine
// própria — o chamador que a iniciou também só espera por done/ctx, então um
// cancelamento de TAB não precisa esperar a varredura terminar.
func (ix *completionPathIndex) get(ctx context.Context, dirs []string, scan func(context.Context, []string) []string) []string {
	if ctx == nil {
		ctx = context.Background()
	}
	key := strings.Join(dirs, "\x00")

	ix.mu.Lock()
	if ix.names != nil && ix.key == key && ix.now().Sub(ix.fetched) < ix.ttl {
		nomes := ix.names
		ix.mu.Unlock()
		return nomes
	}
	if v := ix.inflight; v != nil && ix.flightKey == key {
		ix.mu.Unlock()
		return aguardarCompletionPathScan(ctx, v)
	}
	if err := ctx.Err(); err != nil {
		ix.mu.Unlock()
		return nil
	}
	// A varredura usa um contexto próprio: o cancelamento de UM pedido não pode
	// abortar o cache que os demais aguardam. O teto real é por diretório.
	scanCtx, cancel := context.WithTimeout(context.Background(), completionPathScanTimeout)
	v := &completionPathScan{done: make(chan struct{})}
	ix.inflight, ix.flightKey = v, key
	// O I/O acontece fora do mutex, em goroutine própria.
	go func() {
		names := scan(scanCtx, dirs)
		cancel()
		ix.mu.Lock()
		if ix.inflight == v {
			ix.inflight, ix.flightKey = nil, ""
		}
		ix.key, ix.names, ix.fetched = key, names, ix.now()
		v.names = names
		ix.mu.Unlock()
		close(v.done)
	}()
	ix.mu.Unlock()
	return aguardarCompletionPathScan(ctx, v)
}

// aguardarCompletionPathScan espera a varredura compartilhada ou o contexto.
func aguardarCompletionPathScan(ctx context.Context, v *completionPathScan) []string {
	select {
	case <-v.done:
		return v.names
	case <-ctx.Done():
		return nil
	}
}

// scanPathExecutables varre os diretórios do PATH e devolve nomes únicos e
// ordenados aceitos por aceita (nil = aceita qualquer arquivo).
//
// Cada diretório é listado em paralelo (até completionPathScanWorkers) com
// timeout curto próprio; diretório inacessível/lento é descartado sem invalidar
// a consulta. Diretórios repetidos são deduplicados sem diferenciar caixa (o FS
// do Windows é case-insensitive). O corte em completionPathMaxEntries acontece
// após deduplicar e ordenar alfabeticamente, de forma determinística.
func scanPathExecutables(ctx context.Context, dirs []string, listDir func(string) ([]completionFSEntry, error), aceita func(string) bool, dirTimeout time.Duration) []string {
	if ctx == nil {
		ctx = context.Background()
	}
	if dirTimeout <= 0 {
		dirTimeout = completionPathDirTimeout
	}
	unicos := dedupeCompletionPathDirs(dirs)

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, completionPathScanWorkers)
	)
	nomes := make([]string, 0, len(unicos))
	vistos := make(map[string]struct{})

loop:
	for _, dir := range unicos {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			defer func() { <-sem }()
			entries, err := listCompletionDirComTimeout(ctx, listDir, dir, dirTimeout)
			if err != nil {
				return // diretório do PATH inacessível não invalida a consulta
			}
			mu.Lock()
			defer mu.Unlock()
			for _, e := range entries {
				if e.IsDir {
					continue
				}
				if aceita != nil && !aceita(e.Name) {
					continue
				}
				chave := strings.ToLower(e.Name)
				if _, ok := vistos[chave]; ok {
					continue
				}
				vistos[chave] = struct{}{}
				nomes = append(nomes, e.Name)
			}
		}(dir)
	}
	wg.Wait()

	ordenarCompletionPathNames(nomes)
	if len(nomes) > completionPathMaxEntries {
		nomes = nomes[:completionPathMaxEntries]
	}
	return nomes
}

// dedupeCompletionPathDirs remove diretórios vazios/repetidos preservando a
// ordem e a grafia da primeira ocorrência. A comparação ignora caixa (Windows).
func dedupeCompletionPathDirs(dirs []string) []string {
	vistos := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		chave := strings.ToLower(filepath.Clean(dir))
		if _, ok := vistos[chave]; ok {
			continue
		}
		vistos[chave] = struct{}{}
		out = append(out, dir)
	}
	return out
}

// ordenarCompletionPathNames ordena alfabeticamente sem diferenciar caixa
// (ordem do Explorer), com desempate pela grafia original para ser determinística.
func ordenarCompletionPathNames(nomes []string) {
	sort.Slice(nomes, func(i, j int) bool {
		a, b := strings.ToLower(nomes[i]), strings.ToLower(nomes[j])
		if a == b {
			return nomes[i] < nomes[j]
		}
		return a < b
	})
}

// listCompletionDirComTimeout lista um diretório com teto próprio. O ReadDir
// real não aceita contexto, então ele roda em goroutine e é abandonado no
// timeout (a goroutine some quando o SO devolver o controle).
func listCompletionDirComTimeout(ctx context.Context, listDir func(string) ([]completionFSEntry, error), dir string, timeout time.Duration) ([]completionFSEntry, error) {
	if timeout <= 0 {
		timeout = completionPathDirTimeout
	}
	listCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type resultado struct {
		entries []completionFSEntry
		err     error
	}
	ch := make(chan resultado, 1)
	go func() {
		entries, err := listDir(dir)
		ch <- resultado{entries: entries, err: err}
	}()

	select {
	case r := <-ch:
		return r.entries, r.err
	case <-listCtx.Done():
		return nil, listCtx.Err()
	}
}

// dedupeCompletionMatches remove repetições de Text preservando a ordem.
func dedupeCompletionMatches(matches []completionMatch) []completionMatch {
	if len(matches) < 2 {
		return matches
	}
	vistos := make(map[string]struct{}, len(matches))
	out := matches[:0]
	for _, m := range matches {
		if _, ok := vistos[m.Text]; ok {
			continue
		}
		vistos[m.Text] = struct{}{}
		out = append(out, m)
	}
	return out
}
