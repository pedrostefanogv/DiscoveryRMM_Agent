package app

// Autenticação e proteção do canal IPC (dois pipes + token de sessão).
//
// Este arquivo é propositalmente independente de plataforma: a classificação
// de métodos, o armazenamento de tokens, o rate limit e o dedup de cid são
// testáveis sem named pipe. A validação do CHAMADOR (SID/PID/sessão) e o
// listener do pipe admin vivem em ipc_windows.go, onde as APIs do Windows
// estão disponíveis.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// IPCPipeAdminName é o canal administrativo/mutante. Diferente do pipe de
// leitura, ele exige validação do processo chamador e token de sessão para
// cada request mutante. A UI antiga continua usando somente IPCPipeName.
const IPCPipeAdminName = `\.pipediscovery-agent-ipc-admin`

const (
	// ipcAuthTokenTTL é a validade do token emitido no hello do pipe admin.
	// hello no mesmo pipe renova (rotaciona) o token.
	ipcAuthTokenTTL = 12 * time.Hour

	// Limites por conexão (C3).
	ipcMaxRequestsPerSecond = 50
	ipcMaxRequestsInFlight  = 8
	ipcMaxConnections       = 32

	// ipcMaxAdminConnections limita SÓ o canal admin/mutante (DoS, A.3.2): o
	// global de 32 continua valendo para o canal de leitura. A UI abre a
	// conexão admin sob demanda e a fecha quando fica ociosa, então 8 é folgado.
	ipcMaxAdminConnections = 8

	// ipcAdminIdleTimeout é a ociosidade da CONEXÃO admin. O token continua
	// com TTL de 12h (ipcAuthTokenTTL); o que expira aqui é a conexão parada —
	// a UI reabre sob demanda e recebe um token novo.
	ipcAdminIdleTimeout = 5 * time.Minute

	// ipcMaxHellosPerSecond limita hello POR CONEXÃO: cada hello emite (ou
	// rotaciona) o token admin, então uma rajada não pode ser ilimitada. O
	// probe IsServicePresent abre uma conexão nova e manda UM hello — sempre
	// passa.
	ipcMaxHellosPerSecond = 10

	// janela do dedup de "cid" por conexão.
	ipcDedupWindow = 60 * time.Second

	// Retenção do vínculo notificationId → sessão destinatária (S3).
	ipcNotificationTargetTTL = time.Hour
)

// Erros de autenticação/limite. As mensagens são propagadas no campo "error"
// da resposta JSON-lines (o "code" fica em ipc_unauthorized / ipc_rate_limited).
var (
	errIPCTokenMissing = errors.New("token ausente")
	errIPCTokenInvalid = errors.New("token inválido")
	errIPCTokenExpired = errors.New("token expirado")
	errIPCTokenOther   = errors.New("token pertence a outra conexão")
	errIPCTokenBinding = errors.New("token não corresponde ao chamador")
	errIPCWrongChannel = errors.New("método não permitido neste canal")
	errIPCRateLimited  = errors.New("rate limit por conexão excedido")
	errIPCInFlight     = errors.New("limite de requests em voo por conexão excedido")
)

// ipcAuthIdentity identifica o processo do outro lado do pipe. Valid=false
// significa que o aceite não conseguiu provar a identidade (no pipe admin isso
// é motivo para fechar a conexão).
type ipcAuthIdentity struct {
	SID       string
	PID       uint32
	SessionID uint32
	System    bool
	Valid     bool
	// ImagePath/UIImage (A.0.1): caminho da imagem do processo chamador e se
	// ele é a UI (discovery-agent.exe sob um diretório de instalação). Só a UI
	// pode receber token admin; no canal de leitura o chamador não-UI continua
	// conectado, mas NÃO conta como identidade autenticada.
	ImagePath string
	UIImage   bool
}

// String devolve os campos no formato chave=valor usado no log estruturado.
func (id ipcAuthIdentity) String() string {
	return fmt.Sprintf("sid=%q pid=%d session=%d system=%t valid=%t",
		id.SID, id.PID, id.SessionID, id.System, id.Valid)
}

// ── Restrição de chamador: só a UI recebe token admin (A.0.1) ──────────────

// ipcUIExecutableName é o único nome de imagem autorizado a receber token
// admin. A UI instalada é sempre discovery-agent.exe.
const ipcUIExecutableName = "discovery-agent.exe"

// ipcCallerBypassEnv é o bypass EXPLÍCITO de desenvolvimento: qualquer valor
// diferente de "1" mantém a validação ligada.
const ipcCallerBypassEnv = "DISCOVERY_IPC_ALLOW_ANY_CALLER"

// ipcAllowedUIDirectories devolve os diretórios de instalação aceitos para a
// UI. Lidos do ambiente a cada checagem (barato e testável com t.Setenv).
func ipcAllowedUIDirectories() []string {
	var roots []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if base := strings.TrimSpace(os.Getenv(env)); base != "" {
			roots = append(roots, filepath.Join(base, "Discovery"))
		}
	}
	if base := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); base != "" {
		roots = append(roots, filepath.Join(base, "Programs", "Discovery"))
	}
	return roots
}

// ipcIsDiscoveryUIImagePath decide se imagePath é discovery-agent.exe DENTRO de
// um dos diretórios permitidos. Comparação case-insensitive (Windows) e com
// separador de caminho, para "Discovery2" NÃO casar com "Discovery".
func ipcIsDiscoveryUIImagePath(imagePath string, roots []string) bool {
	clean := filepath.Clean(strings.TrimSpace(imagePath))
	if clean == "" || clean == "." {
		return false
	}
	if !strings.EqualFold(filepath.Base(clean), ipcUIExecutableName) {
		return false
	}
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." {
			continue
		}
		prefix := root + string(os.PathSeparator)
		if len(clean) > len(prefix) && strings.EqualFold(clean[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// ipcCallerBypassEnabled informa se o bypass de dev está ligado.
func ipcCallerBypassEnabled() bool {
	return strings.TrimSpace(os.Getenv(ipcCallerBypassEnv)) == "1"
}

// ipcAdminCallerAllowed decide se o chamador pode receber token admin. Sem
// bypass, exige UIImage (validado pelo ipcCallerValidator com
// QueryFullProcessImageName). O bypass é logado como AVISO em cada handshake.
func ipcAdminCallerAllowed(id ipcAuthIdentity) bool {
	if id.UIImage {
		return true
	}
	if ipcCallerBypassEnabled() {
		logIPCStructured("caller_validation_bypassed",
			fmt.Sprintf("caller validation bypassed (dev) env=%s pid=%d image=%q",
				ipcCallerBypassEnv, id.PID, id.ImagePath))
		return true
	}
	return false
}

// ipcRandomToken gera 32 bytes aleatórios em hex (crypto/rand).
func ipcRandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ipcAuthTokenEntry é o vínculo de um token com a conexão/chamador que o
// recebeu. O ConnKey impede que um token vaze para outra conexão do mesmo
// usuário (mesmo SID/PID/sessão).
type ipcAuthTokenEntry struct {
	ConnKey   string
	SID       string
	PID       uint32
	SessionID uint32
	ExpiresAt time.Time
}

// ipcTokenStore guarda os tokens emitidos no pipe admin.
type ipcTokenStore struct {
	mu     sync.Mutex
	ttl    time.Duration
	now    func() time.Time
	tokens map[string]ipcAuthTokenEntry
}

func newIPCTokenStore() *ipcTokenStore {
	return &ipcTokenStore{
		ttl:    ipcAuthTokenTTL,
		now:    time.Now,
		tokens: make(map[string]ipcAuthTokenEntry),
	}
}

func (s *ipcTokenStore) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// issue cria um token novo e invalida os anteriores da MESMA conexão
// (renovação por hello). Devolve token e expiração.
func (s *ipcTokenStore) issue(connKey string, id ipcAuthIdentity) (string, time.Time, error) {
	token, err := ipcRandomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.clock()
	expires := now.Add(s.ttl)
	s.mu.Lock()
	for k, v := range s.tokens {
		if v.ConnKey == connKey {
			delete(s.tokens, k)
		}
	}
	s.tokens[token] = ipcAuthTokenEntry{
		ConnKey:   connKey,
		SID:       id.SID,
		PID:       id.PID,
		SessionID: id.SessionID,
		ExpiresAt: expires,
	}
	s.mu.Unlock()
	return token, expires, nil
}

// validate confere existência, expiração, conexão e binding (SID/PID/sessão).
func (s *ipcTokenStore) validate(token, connKey string, id ipcAuthIdentity) (ipcAuthTokenEntry, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return ipcAuthTokenEntry{}, errIPCTokenMissing
	}
	s.mu.Lock()
	entry, ok := s.tokens[token]
	if ok && !s.clock().Before(entry.ExpiresAt) {
		delete(s.tokens, token)
		s.mu.Unlock()
		return ipcAuthTokenEntry{}, errIPCTokenExpired
	}
	s.mu.Unlock()
	if !ok {
		return ipcAuthTokenEntry{}, errIPCTokenInvalid
	}
	if entry.ConnKey != connKey {
		return ipcAuthTokenEntry{}, errIPCTokenOther
	}
	if entry.SID != id.SID || entry.PID != id.PID || entry.SessionID != id.SessionID {
		return ipcAuthTokenEntry{}, errIPCTokenBinding
	}
	return entry, nil
}

// invalidateConn remove os tokens de uma conexão que caiu.
func (s *ipcTokenStore) invalidateConn(connKey string) {
	s.mu.Lock()
	for k, v := range s.tokens {
		if v.ConnKey == connKey {
			delete(s.tokens, k)
		}
	}
	s.mu.Unlock()
}

// ── Classificação de métodos (mutante x leitura) ───────────────────────────

// ipcKnownMutatingMethods lista os métodos mutantes conhecidos do contrato.
// Os sufixos genéricos abaixo cobrem métodos novos sem alterar esta tabela.
var ipcKnownMutatingMethods = map[string]struct{}{
	"debug:set":                           {},
	"config:set":                          {},
	"memory:add":                          {},
	"memory:delete":                       {},
	"updates:scan":                        {},
	"knowledge:refresh":                   {},
	"support:create":                      {},
	"support:comment_add":                 {},
	"support:comment_add_legacy":          {},
	"support:close":                       {},
	"support:reopen":                      {},
	"support:rate":                        {},
	"support:agent_ticket_create":         {},
	"support:agent_ticket_close":          {},
	"support:agent_comment_add":           {},
	"support:ticket_attachment_upload":    {},
	"support:comment_attachment_upload":   {},
	"support:ticket_attachment_save":      {},
	"support:comment_attachment_save":     {},
	"support:ticket_attachment_settings":  {},
	"support:comment_attachment_settings": {},
}

// isIPCMutatingMethod classifica um método RPC como mutante (exige canal
// admin + token) ou leitura. Tudo que não casa é leitura — preserva o caminho
// de degradação "método desconhecido" para UIs antigas.
func isIPCMutatingMethod(method string) bool {
	m := strings.ToLower(strings.TrimSpace(method))
	if m == "" {
		return false
	}
	if _, ok := ipcKnownMutatingMethods[m]; ok {
		return true
	}
	suffix := m
	if i := strings.LastIndexAny(m, ":."); i >= 0 {
		suffix = m[i+1:]
	}
	switch suffix {
	case "set", "write", "delete", "create", "add", "upload", "save", "refresh", "scan":
		return true
	default:
		return false
	}
}

// isIPCMutatingRequest refina a classificação pelo PAYLOAD: alguns métodos são
// leitura no caminho comum mas MUTAM quando um flag do pedido é ligado.
// Caso real (revisão 2026-10-17): store:catalog com force=true dispara refresh
// (HTTP + regravação do cache de política) e passava pelo canal de leitura sem
// token — mutação disfarçada de leitura.
func isIPCMutatingRequest(method string, payload map[string]any) bool {
	if isIPCMutatingMethod(method) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "store:catalog":
		if raw, ok := payload["force"]; ok {
			if b, isBool := raw.(bool); isBool && b {
				return true
			}
		}
	}
	return false
}

// ipcMethodRequiresAdmin é o nome usado pelo gate do dispatch (ipc_rpc.go).
func ipcMethodRequiresAdmin(method string) bool { return isIPCMutatingMethod(method) }

// ipcRequestRequiresAdmin decide canal/token pelo método + payload (gate do App).
func ipcRequestRequiresAdmin(method string, payload map[string]any) bool {
	return isIPCMutatingRequest(method, payload)
}

// ── Contexto autenticado do dispatch ───────────────────────────────────────

type ipcAuthContextKey struct{}

// ipcRequestAuth é anexado ao context dos requests mutantes já autenticados.
// O gate em handleIPCRequest recusa métodos mutantes sem este contexto.
type ipcRequestAuth struct {
	ConnKey   string
	SID       string
	PID       uint32
	SessionID uint32
	Admin     bool
}

func withIPCAuth(ctx context.Context, auth ipcRequestAuth) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ipcAuthContextKey{}, auth)
}

func ipcAuthFromContext(ctx context.Context) (ipcRequestAuth, bool) {
	if ctx == nil {
		return ipcRequestAuth{}, false
	}
	auth, ok := ctx.Value(ipcAuthContextKey{}).(ipcRequestAuth)
	return auth, ok
}

// ── Rate limit por conexão ─────────────────────────────────────────────────

// ipcRequestLimiter implementa janela deslizante de 1s (50 req/s) e teto de
// 8 requests em voo por conexão. begin devolve uma função de release; quando
// recusa, NENHUMA goroutine de dispatch é criada.
type ipcRequestLimiter struct {
	mu          sync.Mutex
	now         func() time.Time
	window      []time.Time
	inFlight    int
	maxPerSec   int
	maxInFlight int
}

func newIPCRequestLimiter() *ipcRequestLimiter {
	return &ipcRequestLimiter{
		now:         time.Now,
		maxPerSec:   ipcMaxRequestsPerSecond,
		maxInFlight: ipcMaxRequestsInFlight,
	}
}

// newIPCHelloLimiter limita hellos por conexão (emissão/rotação de token).
// Reusa a janela deslizante do limiter de requests com um teto próprio.
func newIPCHelloLimiter() *ipcRequestLimiter {
	return &ipcRequestLimiter{
		now:         time.Now,
		maxPerSec:   ipcMaxHellosPerSecond,
		maxInFlight: ipcMaxRequestsInFlight,
	}
}

func (l *ipcRequestLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *ipcRequestLimiter) begin() (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	cutoff := now.Add(-time.Second)
	kept := l.window[:0]
	for _, t := range l.window {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.window = kept
	if l.inFlight >= l.maxInFlight {
		return nil, errIPCInFlight
	}
	if len(l.window) >= l.maxPerSec {
		return nil, errIPCRateLimited
	}
	l.window = append(l.window, now)
	l.inFlight++
	var released atomic.Bool
	return func() {
		if released.CompareAndSwap(false, true) {
			l.mu.Lock()
			if l.inFlight > 0 {
				l.inFlight--
			}
			l.mu.Unlock()
		}
	}, nil
}

// ── Dedup de cid por conexão ───────────────────────────────────────────────

type ipcDedupEntry struct {
	createdAt time.Time
	response  map[string]any
}

// ipcDedupCache evita reexecutar um request mutante repetido com o mesmo cid
// dentro de 60s. Se a primeira execução ainda estiver em voo, a repetição é
// ignorada (não despacha outra goroutine).
type ipcDedupCache struct {
	mu      sync.Mutex
	now     func() time.Time
	window  time.Duration
	entries map[string]*ipcDedupEntry
}

func newIPCDedupCache() *ipcDedupCache {
	return &ipcDedupCache{
		now:     time.Now,
		window:  ipcDedupWindow,
		entries: make(map[string]*ipcDedupEntry),
	}
}

func (d *ipcDedupCache) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// begin registra o cid. duplicate=true quando ele já existe na janela; nesse
// caso cached pode trazer a resposta já emitida (ou nil se ainda em voo).
func (d *ipcDedupCache) begin(cid string) (cached map[string]any, duplicate bool) {
	cid = strings.TrimSpace(cid)
	if cid == "" {
		return nil, false
	}
	now := d.clock()
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, e := range d.entries {
		if now.Sub(e.createdAt) > d.window {
			delete(d.entries, k)
		}
	}
	if e, ok := d.entries[cid]; ok {
		return e.response, true
	}
	d.entries[cid] = &ipcDedupEntry{createdAt: now}
	return nil, false
}

// finish associa a resposta emitida ao cid (dedup devolve a mesma resposta).
func (d *ipcDedupCache) finish(cid string, response map[string]any) {
	cid = strings.TrimSpace(cid)
	if cid == "" {
		return
	}
	d.mu.Lock()
	if e, ok := d.entries[cid]; ok {
		e.response = response
	}
	d.mu.Unlock()
}

// ── Vínculo notificationId → sessão destinatária (S3) ──────────────────────

type ipcNotificationTarget struct {
	SessionID  uint32
	SID        string
	AnySession bool
	CreatedAt  time.Time
}

var ipcNotificationTargets = struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]ipcNotificationTarget
}{
	now:     time.Now,
	entries: make(map[string]ipcNotificationTarget),
}

func ipcTargetClock() time.Time {
	if ipcNotificationTargets.now != nil {
		return ipcNotificationTargets.now()
	}
	return time.Now()
}

// ipcRememberNotificationTarget persiste o destinatário do prompt. anySession
// é o fallback quando o serviço não consegue resolver a sessão ativa (ex.:
// sessão RDP sem console): ainda exige chamador AUTENTICADO, mas não amarra a
// uma sessão específica.
func ipcRememberNotificationTarget(notificationID string, id ipcAuthIdentity, anySession bool) {
	notificationID = strings.TrimSpace(notificationID)
	if notificationID == "" {
		return
	}
	now := ipcTargetClock()
	ipcNotificationTargets.mu.Lock()
	for k, v := range ipcNotificationTargets.entries {
		if now.Sub(v.CreatedAt) > ipcNotificationTargetTTL {
			delete(ipcNotificationTargets.entries, k)
		}
	}
	ipcNotificationTargets.entries[notificationID] = ipcNotificationTarget{
		SessionID:  id.SessionID,
		SID:        id.SID,
		AnySession: anySession || !id.Valid,
		CreatedAt:  now,
	}
	ipcNotificationTargets.mu.Unlock()
}

// ipcNotificationTargetAllows decide se o chamador pode responder ao prompt.
// SYSTEM sempre pode; fora isso exige identidade validada e a sessão (e o SID,
// quando conhecido) do destinatário.
func ipcNotificationTargetAllows(notificationID string, caller ipcAuthIdentity) bool {
	notificationID = strings.TrimSpace(notificationID)
	if notificationID == "" {
		return false
	}
	if caller.System {
		return true
	}
	if !caller.Valid {
		return false
	}
	ipcNotificationTargets.mu.Lock()
	target, ok := ipcNotificationTargets.entries[notificationID]
	ipcNotificationTargets.mu.Unlock()
	if !ok {
		return false
	}
	if ipcTargetClock().Sub(target.CreatedAt) > ipcNotificationTargetTTL {
		return false
	}
	if target.AnySession {
		return true
	}
	if target.SessionID != caller.SessionID {
		return false
	}
	if target.SID != "" && caller.SID != "" && target.SID != caller.SID {
		return false
	}
	return true
}

// ── Autorização de remote_session (S2) ─────────────────────────────────────

// ipcRemoteSessionAllowed exige canal admin, token validado e vínculo com o
// chamador. Campos numéricos de identidade no payload (pid/sessionId/
// targetSessionId) precisam bater com a conexão autenticada.
func ipcRemoteSessionAllowed(identity ipcAuthIdentity, admin, tokenValid bool, payload map[string]any) error {
	if !admin {
		return errIPCWrongChannel
	}
	if !tokenValid || !identity.Valid {
		return errIPCTokenMissing
	}
	checkNumeric := func(key string) error {
		raw, ok := payload[key]
		if !ok {
			return nil
		}
		switch v := raw.(type) {
		case float64:
			if key == "pid" {
				if uint32(v) != identity.PID {
					return errIPCTokenBinding
				}
				return nil
			}
			if uint32(v) != identity.SessionID {
				return errIPCTokenBinding
			}
		case int:
			if key == "pid" {
				if uint32(v) != identity.PID {
					return errIPCTokenBinding
				}
				return nil
			}
			if uint32(v) != identity.SessionID {
				return errIPCTokenBinding
			}
		}
		return nil
	}
	if err := checkNumeric("pid"); err != nil {
		return err
	}
	if err := checkNumeric("sessionId"); err != nil {
		return err
	}
	if err := checkNumeric("targetSessionId"); err != nil {
		return err
	}
	return nil
}

// ── Contadores/auditoria ───────────────────────────────────────────────────

type ipcAuthMetrics struct {
	MutatingRequests     atomic.Int64
	MutatingAllowed      atomic.Int64
	MutatingDenied       atomic.Int64
	Unauthorized         atomic.Int64
	RateLimited          atomic.Int64
	InFlightLimited      atomic.Int64
	DuplicateCID         atomic.Int64
	CallerValidationFail atomic.Int64
	RemoteSessionDenied  atomic.Int64
	NotificationDenied   atomic.Int64
	TokenIssued          atomic.Int64
	CallerNotAllowed     atomic.Int64
	HelloRateLimited     atomic.Int64
	AdminConnRefused     atomic.Int64
	AdminIdleClosed      atomic.Int64
	MutatingSucceeded    atomic.Int64
	MutatingFailed       atomic.Int64
}

var ipcAuthStats ipcAuthMetrics

// ipcAuditMutating registra o log estruturado de um request mutante.
func ipcAuditMutating(method string, id ipcAuthIdentity, result string) {
	ipcAuthStats.MutatingRequests.Add(1)
	if result == "allowed" {
		ipcAuthStats.MutatingAllowed.Add(1)
	} else {
		ipcAuthStats.MutatingDenied.Add(1)
	}
	logIPCStructured("mutating_request", fmt.Sprintf("method=%q %s result=%s", method, id.String(), result))
}

// ipcAuditMutatingOutcome (A.0.8) registra o DESFECHO REAL do request mutante,
// que o audit de gate ("allowed") não conhece: o gate roda ANTES da execução.
// Loga sid/pid/sessionId/method/ok|code/duração — sem tocar no contrato.
func ipcAuditMutatingOutcome(method string, auth ipcRequestAuth, ok bool, code string, dur time.Duration) {
	result := "ok"
	if !ok {
		if strings.TrimSpace(code) == "" {
			code = "error"
		}
		result = code
	}
	if ok {
		ipcAuthStats.MutatingSucceeded.Add(1)
	} else {
		ipcAuthStats.MutatingFailed.Add(1)
	}
	logIPCStructured("mutating_outcome", fmt.Sprintf(
		"sid=%q pid=%d sessionId=%d method=%q result=%s duration=%s",
		auth.SID, auth.PID, auth.SessionID, method, result, dur.Round(time.Millisecond)))
}

// ipcStructuredLogger é substituível em testes para não poluir a saída.
var ipcStructuredLogger = func(scope, message string) {
	log.Printf("[%s] %s", scope, message)
}

// logIPCStructured mantém o formato chave=valor (fácil de filtrar no logs.db).
func logIPCStructured(event, fields string) {
	ipcStructuredLogger("ipc", event+" "+fields)
}
