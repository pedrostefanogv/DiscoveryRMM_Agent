package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	osquery "github.com/osquery/osquery-go"

	"discovery/app/core/models"
	"discovery/app/core/processutil"
)

// osqueryQuery describes a single osquery SQL query to execute.
type osqueryQuery struct {
	name     string // human-readable label (for error messages)
	sql      string
	required bool // if true, an error is propagated to the caller
}

// osqueryResult holds the output of a single osquery query.
type osqueryResult struct {
	name string
	rows []map[string]any
	err  error
}

// queryOsquery executes a single osquery query using the given binary.
// The provided context controls the subprocess lifetime; the caller is
// responsible for setting an appropriate deadline.
func queryOsquery(ctx context.Context, binary, query string) ([]map[string]any, error) {
	cmd := exec.CommandContext(ctx, binary, "--json", query)
	processutil.HideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("erro no osqueryi: %w | saida: %s", err, strings.TrimSpace(string(output)))
	}

	var rows []map[string]any
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, fmt.Errorf("erro ao parsear json do osqueryi: %w", err)
	}
	return rows, nil
}

// maxConcurrentQueries limits the number of goroutines spawned during
// parallel osquery execution to prevent resource exhaustion.
const maxConcurrentQueries = 6

// runParallelQueries executes all queries concurrently (up to
// maxConcurrentQueries at a time) and returns results keyed by query name.
// The provided context should already have a timeout.
func runParallelQueries(ctx context.Context, binary string, queries []osqueryQuery, progress func()) map[string]osqueryResult {
	results := make([]osqueryResult, len(queries))
	var wg sync.WaitGroup
	wg.Add(len(queries))

	if progress != nil {
		progress()
	}

	sem := make(chan struct{}, maxConcurrentQueries)

	for i, q := range queries {
		go func(idx int, query osqueryQuery) {
			defer wg.Done()
			sem <- struct{}{}        // acquire
			defer func() { <-sem }() // release
			rows, err := queryOsquery(ctx, binary, query.sql)
			results[idx] = osqueryResult{name: query.name, rows: rows, err: err}
			if progress != nil {
				progress()
			}
		}(i, q)
	}

	wg.Wait()

	m := make(map[string]osqueryResult, len(results))
	for _, r := range results {
		m[r.name] = r
	}
	return m
}

const (
	osqueryPositiveCacheTTL = 10 * time.Minute
	osqueryNegativeCacheTTL = 15 * time.Second
)

type osqueryBinaryCache struct {
	mu        sync.RWMutex
	path      string
	err       error
	checkedAt time.Time
}

var osqueryCache osqueryBinaryCache

// FindOsqueryBinary attempts to locate the osqueryi executable.
// Results are cached with TTL and can be invalidated after install.
func FindOsqueryBinary() (string, error) {
	now := time.Now()

	osqueryCache.mu.RLock()
	path := osqueryCache.path
	err := osqueryCache.err
	checkedAt := osqueryCache.checkedAt
	osqueryCache.mu.RUnlock()

	if !checkedAt.IsZero() {
		age := now.Sub(checkedAt)
		if err == nil && path != "" {
			if age < osqueryPositiveCacheTTL {
				if _, statErr := os.Stat(path); statErr == nil {
					return path, nil
				}
			}
		} else if age < osqueryNegativeCacheTTL {
			return "", err
		}
	}

	resolvedPath, resolveErr := resolveOsqueryBinary()
	osqueryCache.mu.Lock()
	osqueryCache.path = resolvedPath
	osqueryCache.err = resolveErr
	osqueryCache.checkedAt = now
	osqueryCache.mu.Unlock()

	return resolvedPath, resolveErr
}

// InvalidateOsqueryBinaryCache forces the next FindOsqueryBinary call to re-check PATH/filesystem.
func InvalidateOsqueryBinaryCache() {
	osqueryCache.mu.Lock()
	osqueryCache.path = ""
	osqueryCache.err = nil
	osqueryCache.checkedAt = time.Time{}
	osqueryCache.mu.Unlock()
}

func resolveOsqueryBinary() (string, error) {
	candidates := []string{
		"osqueryi.exe",
		"osqueryi",
		`C:\\Program Files\\osquery\\osqueryi.exe`,
	}

	for _, c := range candidates {
		if path, err := exec.LookPath(c); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("osqueryi nao encontrado")
}

// GetOsqueryStatus checks whether osqueryi is available on this machine.
func GetOsqueryStatus() models.OsqueryStatus {
	path, err := FindOsqueryBinary()
	if err != nil {
		return models.OsqueryStatus{
			Installed:          false,
			Path:               "",
			SuggestedPackageID: "osquery.osquery",
		}
	}

	return models.OsqueryStatus{
		Installed:          true,
		Path:               path,
		SuggestedPackageID: "osquery.osquery",
	}
}

// osqueryTokenRegexp encontra identificadores (tabelas, colunas, funcoes) na
// consulta para a checagem de objetos proibidos.
var osqueryTokenRegexp = regexp.MustCompile("[A-Za-z_][A-Za-z0-9_]*")

// osqueryStringLiteral casa literais entre aspas simples (com ” escapado).
var osqueryStringLiteral = regexp.MustCompile("'(?:[^']|'')*'")

// forbiddenOsqueryObjects sao tabelas/virtual tables que NAO podem ser usadas
// pela tool osquery: elas leem arquivos arbitrarios ou acessam a rede, o que
// burlaria o gate de autorizacao do read_file.
//
//	read_file -> le qualquer arquivo do disco sem consentimento do usuario
//	curl      -> faz requisicoes HTTP (exfiltracao)
//	attach    -> permite anexar/criar arquivos via SQLite
var forbiddenOsqueryObjects = map[string]struct{}{
	"read_file": {},
	"curl":      {},
	"wget":      {},
	"attach":    {},
	"detach":    {},
}

// ValidateReadOnlyOsqueryQuery garante que a consulta e uma UNICA instrucao
// SELECT/WITH e que nao referencia objetos capazes de ler arquivos ou acessar a
// rede. A validacao roda antes de qualquer contato com o osquery (socket ou
// binario), servindo de gate tambem para chamadas futuras.
func ValidateReadOnlyOsqueryQuery(sql string) error {
	query := strings.TrimSpace(sql)
	if query == "" {
		return fmt.Errorf("query SQL nao pode ser vazia")
	}
	upper := strings.ToUpper(query)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return fmt.Errorf("apenas consultas read-only (SELECT/WITH) sao permitidas")
	}

	// Uma instrucao por consulta: aceita um unico ';' no final. A checagem roda
	// sobre a consulta SEM literais — um ';' dentro de um valor (ex.: SELECT ';')
	// nao e uma segunda instrucao.
	single := strings.TrimSpace(strings.TrimSuffix(query, ";"))

	// Literais de string são removidos antes da varredura: uma consulta que
	// apenas MENCIONA 'read_file' num valor não referencia a tabela. Já os
	// identificadores entre aspas duplas ("read_file") NÃO são removidos, porque
	// em SQLite eles são justamente nomes de tabela/coluna.
	scrubbed := osqueryStringLiteral.ReplaceAllString(single, "''")

	if strings.Contains(scrubbed, ";") {
		return fmt.Errorf("apenas uma instrucao SQL por consulta")
	}

	for _, token := range osqueryTokenRegexp.FindAllString(scrubbed, -1) {
		if _, forbidden := forbiddenOsqueryObjects[strings.ToLower(token)]; forbidden {
			return fmt.Errorf("a tabela/funcao %q nao e permitida na tool osquery (le arquivos ou acessa a rede); use read_file, que exige autorizacao do usuario", token)
		}
	}
	return nil
}

// RunOsqueryQuery executa uma query SQL read-only no osquery e devolve as
// linhas como map[string]string, limitadas a limit (max 200). Usa o cliente
// osquery-go (socket do osqueryd) quando disponivel e cai para o binario
// osqueryi como fallback.
func RunOsqueryQuery(ctx context.Context, sql string, limit int) ([]map[string]string, error) {
	query := strings.TrimSpace(sql)
	if err := ValidateReadOnlyOsqueryQuery(query); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var rows []map[string]string
	usedSocket := false

	// 1) Cliente osquery-go (socket) quando o osqueryd estiver disponivel.
	if socketPath := findOsquerydSocket(); socketPath != "" {
		client, err := osquery.NewClient(socketPath, socketConnectTimeout)
		if err == nil {
			rowsRaw, statusCode, statusMessage, qErr := queryWithRetry(ctx, client, query)
			client.Close()
			switch {
			case qErr != nil:
				return nil, fmt.Errorf("erro ao executar query osquery: %w", qErr)
			case statusCode != 0:
				return nil, fmt.Errorf("osquery erro: %s", statusMessage)
			default:
				rows = rowsRaw
				usedSocket = true
			}
		}
	}

	// 2) Fallback: binario osqueryi.
	if !usedSocket {
		binary, err := FindOsqueryBinary()
		if err != nil {
			return nil, err
		}
		queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		rowsAny, err := queryOsquery(queryCtx, binary, query)
		if err != nil {
			return nil, err
		}
		rows = make([]map[string]string, 0, len(rowsAny))
		for _, row := range rowsAny {
			converted := make(map[string]string, len(row))
			for k, v := range row {
				converted[k] = fmt.Sprintf("%v", v)
			}
			rows = append(rows, converted)
		}
	}

	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}
