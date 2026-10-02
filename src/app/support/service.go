package support

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"discovery/app/core/tlsutil"
	"discovery/app/debug"
	"discovery/app/netutil"
	"discovery/app/supportmeta"
)

var guidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var idempotencyFallbackSeq atomic.Uint64

// newIdempotencyKey gera uma chave (UUID v4) para deduplicação server-side de
// operações mutáveis (create/comment/close). A chave é enviada no header
// Idempotency-Key e o servidor deduplica reenvios com a mesma chave.
func newIdempotencyKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand indisponível: evita colisão sob concorrência com sequência.
		return fmt.Sprintf("%d-%d", time.Now().UnixNano(), idempotencyFallbackSeq.Add(1))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// doPostWithRetry reenvia a MESMA requisição POST (mesma Idempotency-Key) uma vez
// em falha de rede ou HTTP 5xx. É seguro porque o servidor deduplica pela chave.
func doPostWithRetry(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}

		attemptReq := req
		if attempt > 0 {
			if req.GetBody == nil {
				return nil, fmt.Errorf("requisição sem body reconstruível para retry")
			}
			fresh, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			attemptReq = req.Clone(ctx)
			attemptReq.Body = fresh
		}

		resp, err := client.Do(attemptReq)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode >= 500 && attempt == 0 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			_ = resp.Body.Close()
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

// doGetWithRetry repete uma requisição GET uma vez em falha de rede ou HTTP 5xx,
// com backoff de 500ms. O request é reconstruído a cada tentativa.
func doGetWithRetry(ctx context.Context, client *http.Client, buildReq func() (*http.Request, error)) (*http.Response, error) {
	return doGetAttempts(ctx, client, 2, buildReq)
}

// doGetAttempts permite reduzir as tentativas quando já existe fallback local:
// offline, o retry dobra o tempo até o dado em cache aparecer.
func doGetAttempts(ctx context.Context, client *http.Client, attempts int, buildReq func() (*http.Request, error)) (*http.Response, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		// Só descarta a resposta para retry quando ainda existe tentativa
		// seguinte. Com attempts=1 a resposta 5xx precisa chegar ao chamador,
		// senão o status real é perdido (virava "HTTP 500" genérico).
		if resp.StatusCode >= 500 && attempt < attempts-1 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			_ = resp.Body.Close()
			continue
		}
		return resp, nil
	}
	return nil, lastErr
}

type AgentInfo = supportmeta.AgentInfo

type APIWorkflowState = supportmeta.APIWorkflowState

type TicketPriority = supportmeta.TicketPriority

type APITicket = supportmeta.APITicket

type TicketComment = supportmeta.TicketComment

type CreateTicketInput = supportmeta.CreateTicketInput
type TicketTemplateOption = supportmeta.TicketTemplateOption

type TicketDepartmentField = supportmeta.TicketDepartmentField

type TicketFieldValue = supportmeta.TicketFieldValue

type TicketAnswer = supportmeta.TicketAnswer
type TicketTemplateField = supportmeta.TicketTemplateField
type TicketTemplateQuestion = supportmeta.TicketTemplateQuestion

type TicketOptionDepartment = supportmeta.TicketOptionDepartment

type TicketOptionProfile = supportmeta.TicketOptionProfile

type TicketOptions = supportmeta.TicketOptions

type CloseTicketInput = supportmeta.CloseTicketInput

type KnowledgeArticle = supportmeta.KnowledgeArticle

type KnowledgePage = supportmeta.KnowledgePage

// AgentInfoCache handles cached agent identity values.
type AgentInfoCache interface {
	Get() (AgentInfo, bool)
	Set(AgentInfo)
	Invalidate()
}

// CacheDB exposes the cache operations needed by support.
type CacheDB interface {
	CacheGetJSON(key string, out any) (bool, error)
	CacheSetJSON(key string, value any, ttl time.Duration) error
	CacheDelete(key string) error
}

// Options wires the support service.
type Options struct {
	Logf             func(string)
	Ctx              func() context.Context
	DB               CacheDB
	AgentInfo        AgentInfoCache
	DebugConfig      func() debug.Config
	FeatureEnabled   func(*bool) bool
	SupportEnabled   func() *bool
	KnowledgeEnabled func() *bool
	// AgentInfoFallback devolve clientId/siteId persistidos localmente (cache de
	// configuração do agente) quando /configuration está inacessível. É a última
	// linha de defesa para a consulta offline em instalações que ainda não têm a
	// identidade durável gravada no cache do suporte.
	AgentInfoFallback func() (AgentInfo, bool)
}

// Service handles support and knowledge base APIs.
type Service struct {
	logf             func(string)
	ctx              func() context.Context
	db               CacheDB
	agentInfo        AgentInfoCache
	debugConfig      func() debug.Config
	featureEnabled   func(*bool) bool
	supportEnabled   func() *bool
	knowledgeEnabled func() *bool

	agentInfoFallback func() (AgentInfo, bool)

	knowledgeMu          sync.Mutex
	lastKnowledgeRefresh time.Time
	knowledgeRefreshing  bool

	// offline é o último estado conhecido da conexão com o servidor. Offline o
	// agente opera em modo somente consulta (sem abrir/editar chamados).
	offlineMu sync.Mutex
	offline   bool
}

// NewService builds a support service.
func NewService(opts Options) *Service {
	logf := opts.Logf
	if logf == nil {
		logf = func(string) {}
	}
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background
	}
	debugConfig := opts.DebugConfig
	if debugConfig == nil {
		debugConfig = func() debug.Config { return debug.Config{} }
	}
	featureEnabled := opts.FeatureEnabled
	if featureEnabled == nil {
		featureEnabled = func(flag *bool) bool { return flag == nil || *flag }
	}
	supportEnabled := opts.SupportEnabled
	if supportEnabled == nil {
		supportEnabled = func() *bool { return nil }
	}
	knowledgeEnabled := opts.KnowledgeEnabled
	if knowledgeEnabled == nil {
		knowledgeEnabled = func() *bool { return nil }
	}
	return &Service{
		logf:              logf,
		ctx:               ctx,
		db:                normalizeCacheDB(opts.DB),
		agentInfo:         opts.AgentInfo,
		agentInfoFallback: opts.AgentInfoFallback,
		debugConfig:       debugConfig,
		featureEnabled:    featureEnabled,
		supportEnabled:    supportEnabled,
		knowledgeEnabled:  knowledgeEnabled,
	}
}

// SetDB atualiza a referência ao banco de dados de cache.
// Deve ser chamado após a abertura do SQLite, já que o Service
// pode ser construído antes do banco estar disponível.
func (s *Service) SetDB(db CacheDB) {
	s.db = normalizeCacheDB(db)
}

// normalizeCacheDB devolve nil quando db é uma interface com valor tipado nulo
// (ex.: um (*database.DB)(nil) atribuído a CacheDB durante o startup, antes de o
// SQLite abrir). Sem esta normalização a checagem "s.db != nil" passa, mas a
// chamada no ponteiro nulo falha com "database indisponivel" — gerando os
// avisos repetidos de cache do suporte e impedindo o caminho correto de
// "sem cache local".
func normalizeCacheDB(db CacheDB) CacheDB {
	if db == nil {
		return nil
	}
	v := reflect.ValueOf(db)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		if v.IsNil() {
			return nil
		}
	}
	return db
}

func (s *Service) supportLogf(format string, args ...any) {
	s.logf("[support] " + fmt.Sprintf(format, args...))
}

func shortBodyForLog(body []byte) string {
	s := strings.TrimSpace(string(body))
	runes := []rune(s)
	if len(runes) > 400 {
		// Truncar por runes evita cortar um caractere UTF-8 no meio.
		return string(runes[:400]) + "..."
	}
	return s
}

func normalizePriority(v int) int {
	if v < 1 || v > 4 {
		return 2
	}
	return v
}

func priorityIntToLabel(v int) string {
	switch normalizePriority(v) {
	case 1:
		return "Low"
	case 3:
		return "High"
	case 4:
		return "Critical"
	default:
		return "Medium"
	}
}

func priorityLabelToInt(label string) int {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "1", "low", "baixa":
		return 1
	case "3", "high", "alta":
		return 3
	case "4", "critical", "critica", "crítica":
		return 4
	case "2", "medium", "media", "média":
		fallthrough
	default:
		return 2
	}
}

func toInt(values ...any) int {
	for _, v := range values {
		switch n := v.(type) {
		case float64:
			return int(n)
		case float32:
			return int(n)
		case int:
			return n
		case int64:
			return int(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		case string:
			s := strings.TrimSpace(n)
			if s == "" {
				continue
			}
			var parsed int
			if _, err := fmt.Sscanf(s, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func toBool(values ...any) bool {
	for _, v := range values {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			s := strings.ToLower(strings.TrimSpace(b))
			if s == "true" || s == "1" || s == "yes" || s == "sim" {
				return true
			}
			if s == "false" || s == "0" || s == "no" || s == "nao" || s == "não" {
				return false
			}
		case float64:
			return b != 0
		case int:
			return b != 0
		}
	}
	return false
}

func extractAgentInfoFromJSON(body []byte, cfg debug.Config) (AgentInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return AgentInfo{}, fmt.Errorf("resposta inválida de /api/v1/agent-auth/me/configuration: %w", err)
	}

	asMap := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		return m
	}
	getStr := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k]; ok {
				s := strings.TrimSpace(fmt.Sprint(v))
				if s != "" && s != "<nil>" {
					return s
				}
			}
		}
		return ""
	}

	candidates := []map[string]any{raw}
	for _, key := range []string{"data", "agent", "result", "payload"} {
		if m := asMap(raw[key]); m != nil {
			candidates = append(candidates, m)
		}
	}

	info := AgentInfo{}
	for _, c := range candidates {
		if info.AgentID == "" {
			info.AgentID = getStr(c, "agentId", "agentID", "id")
		}
		if info.ClientID == "" {
			info.ClientID = getStr(c, "clientId", "clientID")
		}
		if info.ClientID == "" {
			if client := asMap(c["client"]); client != nil {
				info.ClientID = getStr(client, "id", "clientId", "clientID")
			}
		}
		if info.SiteID == "" {
			info.SiteID = getStr(c, "siteId", "siteID")
		}
		if info.SiteID == "" {
			if site := asMap(c["site"]); site != nil {
				info.SiteID = getStr(site, "id", "siteId", "siteID")
			}
		}
		if info.Hostname == "" {
			info.Hostname = getStr(c, "hostname", "hostName")
		}
		if info.Name == "" {
			info.Name = getStr(c, "displayName", "name")
		}
	}

	if s := strings.TrimSpace(cfg.AgentID); s != "" {
		info.AgentID = s
	}

	info.AgentID = strings.TrimSpace(info.AgentID)
	info.ClientID = strings.TrimSpace(info.ClientID)
	info.SiteID = strings.TrimSpace(info.SiteID)
	info.Hostname = strings.TrimSpace(info.Hostname)
	info.Name = strings.TrimSpace(info.Name)

	return info, nil
}

const (
	// agentInfoCacheTTL é o TTL do cache "quente" da identidade resolvida.
	agentInfoCacheTTL = 24 * time.Hour
	// agentInfoStaleCacheKey guarda a última identidade válida SEM expiração.
	// O cache "agent_info" expira em 24h e o CacheGet apaga a entrada vencida;
	// sem essa cópia durável, um agente offline por mais de um dia perdia
	// clientId/siteId e TANTO os chamados QUANTO a base de conhecimento falhavam
	// em /configuration antes de chegar aos caches de conteúdo.
	agentInfoStaleCacheKey = "agent_info_stale"
	// ticketListBackupTTL: snapshot local da listagem de chamados, sem
	// expiração, para consulta offline. É sobrescrito a cada listagem exitosa.
	ticketListBackupTTL = 0
	// ticketScopeControlKey guarda o escopo local ativo para limpar snapshots
	// órfãos quando o agente muda de servidor/ID (transferência/reinstalação).
	ticketScopeControlKey = "tickets:active_scope"
)

// apiScheme devolve o scheme efetivo da API: prefere o campo legado (já
// sincronizado por normalizeApiScheme) e cai para o canônico APIScheme()
// quando ele está vazio/inválido — antes um ApiScheme vazio gerava "://".
func apiScheme(cfg debug.Config) string {
	if s := strings.TrimSpace(strings.ToLower(cfg.ApiScheme)); s == "http" || s == "https" {
		return s
	}
	return cfg.APIScheme()
}

// ticketListCacheKey identifica a listagem de chamados desta instalação usando
// apenas dados locais (apiScheme/apiServer/agentId) — não depende de
// clientId/siteId, que só existem depois de uma resposta do servidor. Assim o
// backup continua legível mesmo quando /configuration está inacessível.
func ticketListCacheKey(cfg debug.Config) string {
	parts := []string{
		apiScheme(cfg),
		strings.TrimSpace(strings.ToLower(cfg.ApiServer)),
		strings.TrimSpace(strings.ToLower(cfg.AgentID)),
	}
	for i, p := range parts {
		parts[i] = url.QueryEscape(p)
	}
	return "tickets:list:" + strings.Join(parts, ":")
}

// ticketScope é o escopo local (scheme/servidor/agentId) usado nas chaves.
func ticketScope(cfg debug.Config) string {
	return strings.TrimPrefix(ticketListCacheKey(cfg), "tickets:list:")
}

// ticketListBackupKey produz a chave do snapshot offline a partir da chave de
// listagem. Fica FORA do prefixo "tickets:list:" para nunca ser confundido com
// cache quente e sobreviver a limpezas de cache por prefixo.
func ticketListBackupKey(cacheKey string) string {
	return "tickets:backup:" + strings.TrimPrefix(cacheKey, "tickets:")
}

func ticketDetailBackupKey(cfg debug.Config, ticketID string) string {
	return "tickets:backup:detail:" + ticketScope(cfg) + ":" + url.QueryEscape(strings.ToLower(strings.TrimSpace(ticketID)))
}

func ticketCommentsBackupKey(cfg debug.Config, ticketID string) string {
	return "tickets:backup:comments:" + ticketScope(cfg) + ":" + url.QueryEscape(strings.ToLower(strings.TrimSpace(ticketID)))
}

func ticketFieldsBackupKey(cfg debug.Config, ticketID string) string {
	return "tickets:backup:fields:" + ticketScope(cfg) + ":" + url.QueryEscape(strings.ToLower(strings.TrimSpace(ticketID)))
}

func ticketAnswersBackupKey(cfg debug.Config, ticketID string) string {
	return "tickets:backup:answers:" + ticketScope(cfg) + ":" + url.QueryEscape(strings.ToLower(strings.TrimSpace(ticketID)))
}

// saveTicketBackup grava um snapshot local sem expiração (best-effort).
func (s *Service) saveTicketBackup(key string, value any) {
	if s.db == nil {
		return
	}
	if err := s.db.CacheSetJSON(key, value, ticketListBackupTTL); err != nil {
		log.Printf("[support] aviso: falha ao salvar snapshot local (%s): %v", key, err)
	}
}

// saveTicketListSnapshot grava o snapshot da listagem + a data em que foi
// gravado (exibida no aviso de modo somente consulta).
func (s *Service) saveTicketListSnapshot(cacheKey string, tickets []APITicket) {
	backupKey := ticketListBackupKey(cacheKey)
	s.saveTicketBackup(backupKey, tickets)
	if s.db == nil {
		return
	}
	if err := s.db.CacheSetJSON(ticketSnapshotSavedAtKey(backupKey), time.Now().UTC().Format(time.RFC3339), 0); err != nil {
		log.Printf("[support] aviso: falha ao salvar data do snapshot de chamados: %v", err)
	}
}

func ticketSnapshotSavedAtKey(backupKey string) string { return backupKey + ":savedAt" }

func (s *Service) ticketSnapshotSavedAt(cacheKey string) string {
	var savedAt string
	if !s.readTicketBackup(ticketSnapshotSavedAtKey(ticketListBackupKey(cacheKey)), &savedAt) {
		return ""
	}
	return strings.TrimSpace(savedAt)
}

// readTicketBackup lê um snapshot local (stale-if-error).
func (s *Service) readTicketBackup(key string, out any) bool {
	if s.db == nil {
		return false
	}
	found, err := s.db.CacheGetJSON(key, out)
	return err == nil && found
}

// readTicketListBackup lê o snapshot local de chamados (stale-if-error).
func (s *Service) readTicketListBackup(cacheKey string) ([]APITicket, bool) {
	var backup []APITicket
	if !s.readTicketBackup(ticketListBackupKey(cacheKey), &backup) {
		return nil, false
	}
	if backup == nil {
		backup = []APITicket{}
	}
	return backup, true
}

// hasTicketBackup informa se já existe snapshot local para a chave — usado
// para encurtar o timeout/retry quando o fallback offline está disponível.
func (s *Service) hasTicketBackup(key string) bool {
	if s.db == nil {
		return false
	}
	var raw json.RawMessage
	found, err := s.db.CacheGetJSON(key, &raw)
	return err == nil && found
}

func (s *Service) readCachedTicketDetail(key string) (APITicket, bool) {
	var cached APITicket
	if !s.readTicketBackup(key, &cached) || strings.TrimSpace(cached.ID) == "" {
		return APITicket{}, false
	}
	return cached, true
}

func (s *Service) readCachedTicketComments(key string) ([]TicketComment, bool) {
	var cached []TicketComment
	if !s.readTicketBackup(key, &cached) {
		return nil, false
	}
	if cached == nil {
		cached = []TicketComment{}
	}
	return cached, true
}

// cleanupOldTicketScope remove snapshots do escopo anterior quando o agente
// muda de servidor/ID — evita crescimento indefinido do SQLite com dados que
// nunca mais serão lidos.
func (s *Service) cleanupOldTicketScope(scope string) {
	if s.db == nil || strings.TrimSpace(scope) == "" {
		return
	}
	purger, ok := s.db.(CachePurger)
	if !ok {
		return
	}
	var previous string
	if found, err := s.db.CacheGetJSON(ticketScopeControlKey, &previous); err == nil && found {
		previous = strings.TrimSpace(previous)
		if previous != "" && previous != scope {
			for _, prefix := range []string{
				"tickets:list:",
				"tickets:backup:list:",
				"tickets:backup:detail:",
				"tickets:backup:comments:",
				"tickets:backup:fields:",
				"tickets:backup:answers:",
			} {
				if err := purger.CacheDeletePrefix(prefix + previous); err != nil {
					log.Printf("[support] aviso: falha ao limpar cache %s<escopo>: %v", prefix, err)
				}
			}
			s.supportLogf("escopo local de chamados mudou; snapshots antigos removidos")
		}
	}
	_ = s.db.CacheSetJSON(ticketScopeControlKey, scope, 0)
}

// InvalidateAgentContext descarta a identidade resolvida (memória + cache
// quente + cópia durável). Deve ser chamado quando clientId/siteId/agentId
// mudam (transferência de site/reprovisionamento): sem isso a página mostraria,
// por até 24h, conteúdo do escopo antigo.
func (s *Service) InvalidateAgentContext() {
	if s.agentInfo != nil {
		s.agentInfo.Invalidate()
	}
	if s.db == nil {
		return
	}
	for _, key := range []string{"agent_info", agentInfoStaleCacheKey} {
		if err := s.db.CacheDelete(key); err != nil {
			log.Printf("[support] aviso: falha ao invalidar contexto do agente (%s): %v", key, err)
		}
	}
}

// persistStaleAgentInfo grava uma cópia SEM expiração da identidade resolvida
// (clientId/siteId/agentId) para que chamados e knowledge continuem consultáveis
// durante longos períodos offline.
func (s *Service) persistStaleAgentInfo(info AgentInfo) {
	if s.db == nil || strings.TrimSpace(info.ClientID) == "" {
		return
	}
	if err := s.db.CacheSetJSON(agentInfoStaleCacheKey, info, 0); err != nil {
		log.Printf("[support] aviso: falha ao salvar identidade durável (agent_info_stale): %v", err)
	}
}

// readStaleAgentInfo lê a identidade durável gravada sem expiração.
func (s *Service) readStaleAgentInfo() (AgentInfo, bool) {
	if s.db == nil {
		return AgentInfo{}, false
	}
	var cached AgentInfo
	found, err := s.db.CacheGetJSON(agentInfoStaleCacheKey, &cached)
	if err != nil || !found || strings.TrimSpace(cached.ClientID) == "" {
		return AgentInfo{}, false
	}
	return cached, true
}

// offlineAgentInfo resolve a identidade localmente quando o servidor não
// responde: primeiro a cópia durável do próprio cache, depois clientId/siteId
// persistidos na configuração do agente. Sem isso, toda a consulta offline
// (chamados e base de conhecimento) morria em /configuration.
func (s *Service) offlineAgentInfo(cause error) (AgentInfo, error) {
	s.markUnreachable()
	if stale, ok := s.readStaleAgentInfo(); ok {
		s.supportLogf("servidor inacessivel (%v) — usando identidade local em cache (agentId=%s clientId=%s siteId=%s)", cause, stale.AgentID, stale.ClientID, stale.SiteID)
		if s.agentInfo != nil {
			s.agentInfo.Set(stale)
		}
		return stale, nil
	}
	if s.agentInfoFallback != nil {
		if info, ok := s.agentInfoFallback(); ok && strings.TrimSpace(info.ClientID) != "" {
			s.supportLogf("servidor inacessivel (%v) — usando clientId/siteId persistidos da configuração do agente (clientId=%s siteId=%s)", cause, info.ClientID, info.SiteID)
			s.persistStaleAgentInfo(info)
			if s.agentInfo != nil {
				s.agentInfo.Set(info)
			}
			return info, nil
		}
	}
	return AgentInfo{}, cause
}

// ErrOfflineReadOnly é devolvido quando o agente está offline e a operação
// exigiria escrita no servidor. Offline o agente é somente consulta.
var ErrOfflineReadOnly = errors.New("agente offline: modo somente consulta")

func (s *Service) markReachable() {
	s.offlineMu.Lock()
	s.offline = false
	s.offlineMu.Unlock()
}

func (s *Service) markUnreachable() {
	s.offlineMu.Lock()
	s.offline = true
	s.offlineMu.Unlock()
}

func (s *Service) isOffline() bool {
	s.offlineMu.Lock()
	defer s.offlineMu.Unlock()
	return s.offline
}

// ensureOnline bloqueia mutações quando a conexão com o servidor está
// indisponível: offline o agente consulta, mas não abre/edita chamados.
func (s *Service) ensureOnline(action string) error {
	if s.isOffline() {
		return fmt.Errorf("%w: %s indisponível", ErrOfflineReadOnly, action)
	}
	return nil
}

// transportError converte falha de rede em modo somente consulta (marca a
// conexão como indisponível); erros não-rede mantêm a mensagem original.
func (s *Service) transportError(action string, err error) error {
	if err == nil {
		return nil
	}
	if isNetworkError(err) {
		s.markUnreachable()
		return fmt.Errorf("%w: %s (%v)", ErrOfflineReadOnly, action, err)
	}
	return fmt.Errorf("falha ao %s: %w", action, err)
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

// fetchAgentContext resolves clientId/siteId from /api/v1/agent-auth/me (cached).
func (s *Service) fetchAgentContext() (AgentInfo, error) {
	if s.agentInfo != nil {
		if info, ok := s.agentInfo.Get(); ok {
			if strings.TrimSpace(info.ClientID) != "" {
				return info, nil
			}
			s.supportLogf("cache em memória sem clientId; ignorando e recarregando do servidor")
			s.agentInfo.Invalidate()
		}
	}

	if s.db != nil {
		var cached AgentInfo
		found, err := s.db.CacheGetJSON("agent_info", &cached)
		if err == nil && found {
			if strings.TrimSpace(cached.ClientID) != "" {
				// Reforça a cópia durável (sem expiração) usada no fallback offline.
				s.persistStaleAgentInfo(cached)
				if s.agentInfo != nil {
					s.agentInfo.Set(cached)
				}
				return cached, nil
			}
			s.supportLogf("cache SQLite sem clientId; removendo entrada e atualizando do servidor")
			if delErr := s.db.CacheDelete("agent_info"); delErr != nil {
				log.Printf("[support] aviso: falha ao limpar cache SQLite agent_info inválido: %v", delErr)
			}
		}
	}

	cfg := s.debugConfig()
	cfg.ApiServer = strings.TrimSpace(cfg.ApiServer)
	if cfg.ApiServer == "" || strings.TrimSpace(cfg.AuthToken) == "" {
		err := fmt.Errorf("configuração de servidor API incompleta: preencha apiServer e token no Debug")
		s.supportLogf("falha ao resolver contexto do agente: %v", err)
		return AgentInfo{}, err
	}
	scheme := apiScheme(cfg)

	ctx := s.ctxOrBackground()
	target := scheme + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/configuration"
	// Com identidade durável disponível basta uma tentativa curta: offline, o
	// retry só adia a exibição do cache (o fallback cobre a falha).
	attempts, timeout := 2, 10*time.Second
	if _, ok := s.readStaleAgentInfo(); ok {
		attempts, timeout = 1, 4*time.Second
	}
	resp, err := doGetAttempts(ctx, tlsutil.NewHTTPClient(timeout), attempts, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		wrapped := fmt.Errorf("falha ao conectar em %s: %w", target, err)
		s.supportLogf("erro HTTP ao resolver contexto do agente: %v", wrapped)
		return s.offlineAgentInfo(wrapped)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		s.supportLogf("/api/v1/agent-auth/me/configuration retornou erro: %v", wrapped)
		return s.offlineAgentInfo(wrapped)
	}

	info, err := extractAgentInfoFromJSON(body, cfg)
	if err != nil {
		s.supportLogf("falha ao decodificar /api/v1/agent-auth/me/configuration: %v", err)
		return s.offlineAgentInfo(err)
	}
	if info.ClientID == "" {
		err := fmt.Errorf("clientId não retornado por /api/v1/agent-auth/me/configuration: verifique token/escopo do agente")
		s.supportLogf("%v | resposta=%s", err, shortBodyForLog(body))
		return s.offlineAgentInfo(err)
	}

	if s.agentInfo != nil {
		s.agentInfo.Set(info)
	}
	if s.db != nil {
		if err := s.db.CacheSetJSON("agent_info", info, agentInfoCacheTTL); err != nil {
			log.Printf("[support] aviso: falha ao salvar no cache SQLite (agent_info): %v", err)
		}
	}
	s.persistStaleAgentInfo(info)
	s.markReachable()
	s.supportLogf("contexto do agente resolvido: agentId=%s clientId=%s siteId=%s", info.AgentID, info.ClientID, info.SiteID)

	return info, nil
}

// GetAgentInfo resolves and returns the current agent identifiers from the server.
func (s *Service) GetAgentInfo() (AgentInfo, error) {
	return s.fetchAgentContext()
}

// SupportTicketList é o retorno da listagem para o binding da UI: além dos
// chamados, informa se eles vieram do snapshot offline (stale) e quando o
// snapshot foi gravado (CachedAt). Offline a tela entra em modo somente consulta.
type SupportTicketList struct {
	Tickets  []APITicket `json:"tickets"`
	Stale    bool        `json:"stale"`
	CachedAt string      `json:"cachedAt,omitempty"`
}

// GetSupportTickets returns tickets linked to this agent (filtered by agentId).
func (s *Service) GetSupportTickets() ([]APITicket, error) {
	result, err := s.GetSupportTicketList()
	return result.Tickets, err
}

// GetSupportTicketList devolve a listagem + indicador de cache offline.
func (s *Service) GetSupportTicketList() (SupportTicketList, error) {
	if !s.featureEnabled(s.supportEnabled()) {
		s.supportLogf("suporte desabilitado pela configuração do agente")
		return SupportTicketList{Tickets: []APITicket{}}, nil
	}

	s.supportLogf("listando chamados vinculados ao agente")
	cfg := s.debugConfig()
	cacheKey := ticketListCacheKey(cfg)
	s.cleanupOldTicketScope(ticketScope(cfg))

	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao obter contexto para listagem de chamados: %v", err)
		if backup, ok := s.readTicketListBackup(cacheKey); ok {
			s.markUnreachable()
			s.supportLogf("servidor inacessivel (%v) — usando snapshot local de chamados (%d chamado(s))", err, len(backup))
			return SupportTicketList{Tickets: backup, Stale: true, CachedAt: s.ticketSnapshotSavedAt(cacheKey)}, nil
		}
		return SupportTicketList{}, err
	}
	if strings.TrimSpace(info.ClientID) == "" {
		err := fmt.Errorf("clientId não resolvido: verifique a configuração do agente")
		s.supportLogf("%v (agentId=%s)", err, info.AgentID)
		return SupportTicketList{}, err
	}

	ctx := s.ctxOrBackground()
	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets"
	// Com snapshot local, uma tentativa curta basta: offline o retry só adia a
	// exibição dos dados em cache.
	attempts, timeout := 2, 15*time.Second
	// Checagem leve de existência: ler/deserializar o snapshot inteiro só para
	// decidir o timeout era desperdício (a lista pode ter centenas de chamados).
	if s.hasTicketBackup(ticketListBackupKey(cacheKey)) {
		attempts, timeout = 1, 8*time.Second
	}
	resp, err := doGetAttempts(ctx, tlsutil.NewHTTPClient(timeout), attempts, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		wrapped := fmt.Errorf("falha ao buscar chamados: %w", err)
		s.supportLogf("erro HTTP ao listar chamados: %v", wrapped)
		if backup, ok := s.readTicketListBackup(cacheKey); ok {
			s.markUnreachable()
			s.supportLogf("servidor inacessivel (%v) — usando snapshot local de chamados (%d chamado(s))", wrapped, len(backup))
			return SupportTicketList{Tickets: backup, Stale: true, CachedAt: s.ticketSnapshotSavedAt(cacheKey)}, nil
		}
		return SupportTicketList{}, wrapped
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
		s.supportLogf("erro na listagem de chamados: %v", wrapped)
		// Só falha de servidor justifica servir o snapshot: 4xx indica problema
		// real de credencial/rota e não deve ser mascarado por dados antigos.
		if resp.StatusCode >= 500 {
			if backup, ok := s.readTicketListBackup(cacheKey); ok {
				s.markUnreachable()
				s.supportLogf("HTTP %s — usando snapshot local de chamados (%d chamado(s))", resp.Status, len(backup))
				return SupportTicketList{Tickets: backup, Stale: true, CachedAt: s.ticketSnapshotSavedAt(cacheKey)}, nil
			}
		}
		return SupportTicketList{}, wrapped
	}

	var tickets []APITicket
	if err := json.Unmarshal(body, &tickets); err != nil {
		var envelope struct {
			Items []APITicket `json:"items"`
			Data  []APITicket `json:"data"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			if envelope.Items != nil {
				tickets = envelope.Items
			} else {
				tickets = envelope.Data
			}
		} else {
			return SupportTicketList{}, fmt.Errorf("resposta inválida ao listar chamados: %w", err)
		}
	}
	if tickets == nil {
		tickets = []APITicket{}
	}

	s.markReachable()
	// Snapshot offline sem expiração: consulta local quando /configuration ou
	// /tickets estiverem inacessíveis.
	s.saveTicketListSnapshot(cacheKey, tickets)

	s.supportLogf("listagem concluída: %d chamado(s) retornado(s)", len(tickets))
	return SupportTicketList{Tickets: tickets}, nil
}

// GetTicketOptions returns departments and workflow profiles for the agent ticket
// form (department picker). Without a department the server cannot compute SLA.
func (s *Service) GetTicketOptions() (TicketOptions, error) {
	s.supportLogf("carregando opções de chamado (departamentos/perfis)")
	info, err := s.fetchAgentContext()
	if err != nil {
		return TicketOptions{}, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()
	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/options"
	resp, err := doGetWithRetry(ctx, tlsutil.NewHTTPClient(10*time.Second), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, info.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		return TicketOptions{}, fmt.Errorf("falha ao buscar opções de chamado: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TicketOptions{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var options TicketOptions
	if err := json.Unmarshal(body, &options); err != nil {
		return TicketOptions{}, fmt.Errorf("resposta inválida ao buscar opções de chamado: %w", err)
	}
	if options.Departments == nil {
		options.Departments = []TicketOptionDepartment{}
	}
	if options.WorkflowProfiles == nil {
		options.WorkflowProfiles = []TicketOptionProfile{}
	}
	s.supportLogf("opções carregadas: %d departamento(s), %d perfil(is)", len(options.Departments), len(options.WorkflowProfiles))
	return options, nil
}

// GetTicketDepartmentFields retorna os campos personalizados públicos de um
// departamento. Eles valem para todo chamado do departamento (com ou sem
// template) e são validados obrigatoriamente pelo servidor na abertura.
func (s *Service) GetTicketDepartmentFields(departmentID string) ([]TicketDepartmentField, error) {
	departmentID = strings.TrimSpace(departmentID)
	if !guidPattern.MatchString(departmentID) {
		return nil, fmt.Errorf("departmentId inválido")
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()
	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/departments/" + departmentID + "/fields"
	resp, err := doGetWithRetry(ctx, tlsutil.NewHTTPClient(10*time.Second), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("falha ao buscar campos do departamento: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var fields []TicketDepartmentField
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("resposta inválida ao buscar campos do departamento: %w", err)
	}
	if fields == nil {
		fields = []TicketDepartmentField{}
	}
	s.supportLogf("campos do departamento carregados: %d campo(s)", len(fields))
	return fields, nil
}

// GetTicketFields retorna os campos personalizados do departamento do chamado
// com os valores gravados (detalhe do agent, somente leitura).
func (s *Service) GetTicketFields(ticketID string) ([]TicketFieldValue, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return nil, fmt.Errorf("ticketId inválido")
	}

	cfg := s.debugConfig()
	backupKey := ticketFieldsBackupKey(cfg, ticketID)
	ctx := s.ctxOrBackground()
	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" + ticketID + "/fields"
	attempts, timeout := 2, 10*time.Second
	if s.hasTicketBackup(backupKey) {
		attempts, timeout = 1, 6*time.Second
	}
	resp, err := doGetAttempts(ctx, tlsutil.NewHTTPClient(timeout), attempts, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		var cached []TicketFieldValue
		if s.readTicketBackup(backupKey, &cached) {
			s.markUnreachable()
			s.supportLogf("servidor inacessivel (%v) — usando campos locais do chamado %s", err, ticketID)
			return cached, nil
		}
		return nil, fmt.Errorf("falha ao buscar campos do chamado: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			var cached []TicketFieldValue
			if s.readTicketBackup(backupKey, &cached) {
				s.markUnreachable()
				s.supportLogf("HTTP %s — usando campos locais do chamado %s", resp.Status, ticketID)
				return cached, nil
			}
		}
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var fields []TicketFieldValue
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("resposta inválida ao buscar campos do chamado: %w", err)
	}
	if fields == nil {
		fields = []TicketFieldValue{}
	}
	s.markReachable()
	s.saveTicketBackup(backupKey, fields)
	return fields, nil
}

// GetTicketAnswers retorna as respostas do mini questionário do template de um
// chamado (detalhe do agent, somente leitura).
func (s *Service) GetTicketAnswers(ticketID string) ([]TicketAnswer, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return nil, fmt.Errorf("ticketId inválido")
	}

	cfg := s.debugConfig()
	backupKey := ticketAnswersBackupKey(cfg, ticketID)
	ctx := s.ctxOrBackground()
	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" + ticketID + "/answers"
	attempts, timeout := 2, 10*time.Second
	if s.hasTicketBackup(backupKey) {
		attempts, timeout = 1, 6*time.Second
	}
	resp, err := doGetAttempts(ctx, tlsutil.NewHTTPClient(timeout), attempts, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		var cached []TicketAnswer
		if s.readTicketBackup(backupKey, &cached) {
			s.markUnreachable()
			s.supportLogf("servidor inacessivel (%v) — usando respostas locais do chamado %s", err, ticketID)
			return cached, nil
		}
		return nil, fmt.Errorf("falha ao buscar respostas do chamado: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			var cached []TicketAnswer
			if s.readTicketBackup(backupKey, &cached) {
				s.markUnreachable()
				s.supportLogf("HTTP %s — usando respostas locais do chamado %s", resp.Status, ticketID)
				return cached, nil
			}
		}
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var answers []TicketAnswer
	if err := json.Unmarshal(body, &answers); err != nil {
		return nil, fmt.Errorf("resposta inválida ao buscar respostas do chamado: %w", err)
	}
	if answers == nil {
		answers = []TicketAnswer{}
	}
	s.markReachable()
	s.saveTicketBackup(backupKey, answers)
	return answers, nil
}

// CreateSupportTicket opens a new ticket linked to this agent.
func (s *Service) CreateSupportTicket(input CreateTicketInput) (APITicket, error) {
	if !s.featureEnabled(s.supportEnabled()) {
		// Mesmo gate da listagem: não criar chamado com suporte desabilitado.
		s.supportLogf("suporte desabilitado pela configuração do agente")
		return APITicket{}, fmt.Errorf("suporte desabilitado pela configuração do agente")
	}

	if err := s.ensureOnline("abrir chamado"); err != nil {
		s.supportLogf("criação bloqueada: %v", err)
		return APITicket{}, err
	}

	s.supportLogf("criando chamado: title=%q priority=%d category=%q", strings.TrimSpace(input.Title), input.Priority, strings.TrimSpace(input.Category))
	info, err := s.fetchAgentContext()
	if err != nil {
		s.supportLogf("falha ao obter contexto para criação de chamado: %v", err)
		return APITicket{}, err
	}
	if strings.TrimSpace(info.ClientID) == "" {
		err := fmt.Errorf("clientId não resolvido: verifique a configuração do agente")
		s.supportLogf("%v (agentId=%s)", err, info.AgentID)
		return APITicket{}, err
	}
	// A resolução do contexto pode ter descoberto que estamos offline (usou o
	// cache durável). Bloqueia antes de gastar o timeout do POST.
	if err := s.ensureOnline("abrir chamado"); err != nil {
		return APITicket{}, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	type createReq struct {
		DepartmentID      *string        `json:"departmentId,omitempty"`
		WorkflowProfileID *string        `json:"workflowProfileId,omitempty"`
		Title             string         `json:"title"`
		Description       string         `json:"description"`
		Priority          *string        `json:"priority,omitempty"`
		Category          *string        `json:"category,omitempty"`
		TemplateID        *string        `json:"templateId,omitempty"`
		CustomFieldValues map[string]any `json:"customFieldValues,omitempty"`
		TemplateAnswers   map[string]any `json:"templateAnswers,omitempty"`
	}

	payload := createReq{
		Title:       strings.TrimSpace(input.Title),
		Description: strings.TrimSpace(input.Description),
	}
	if t := strings.TrimSpace(input.TemplateID); t != "" {
		payload.TemplateID = &t
	}
	if len(input.CustomFields) > 0 {
		payload.CustomFieldValues = input.CustomFields
	}
	if len(input.TemplateAnswers) > 0 {
		payload.TemplateAnswers = input.TemplateAnswers
	}
	if c := strings.TrimSpace(input.Category); c != "" {
		payload.Category = &c
	}
	if d := strings.TrimSpace(input.DepartmentID); d != "" {
		payload.DepartmentID = &d
	}
	if p := strings.TrimSpace(input.WorkflowProfileID); p != "" {
		payload.WorkflowProfileID = &p
	}
	if input.Priority > 0 {
		pri := priorityIntToLabel(input.Priority)
		payload.Priority = &pri
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		wrapped := fmt.Errorf("erro ao serializar chamado: %w", err)
		s.supportLogf("falha ao serializar payload de chamado: %v", wrapped)
		return APITicket{}, wrapped
	}

	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(reqBody))
	if err != nil {
		wrapped := fmt.Errorf("URL inválida: %w", err)
		s.supportLogf("falha ao montar request de criação: %v", wrapped)
		return APITicket{}, wrapped
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", newIdempotencyKey())
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return APITicket{}, err
	}

	resp, err := doPostWithRetry(ctx, tlsutil.NewHTTPClient(15*time.Second), req)
	if err != nil {
		wrapped := s.transportError("criar chamado", err)
		s.supportLogf("erro HTTP ao criar chamado: %v", wrapped)
		return APITicket{}, wrapped
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		wrapped := fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
		s.supportLogf("erro na criação do chamado: %v | payload=%s | resposta=%s", wrapped, shortBodyForLog(reqBody), shortBodyForLog(respBody))
		return APITicket{}, wrapped
	}

	s.markReachable()
	var ticket APITicket
	if err := json.Unmarshal(respBody, &ticket); err != nil {
		wrapped := fmt.Errorf("resposta inválida ao criar chamado: %w", err)
		s.supportLogf("falha ao decodificar resposta da criação: %v | resposta=%s", wrapped, shortBodyForLog(respBody))
		return APITicket{}, wrapped
	}
	s.supportLogf("chamado criado com sucesso: ticketId=%s", ticket.ID)
	return ticket, nil
}

// GetSupportTicketDetails returns a single ticket if it belongs to the authenticated agent.
func (s *Service) GetSupportTicketDetails(ticketID string) (APITicket, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return APITicket{}, fmt.Errorf("ticketId inválido")
	}

	cfg := s.debugConfig()
	backupKey := ticketDetailBackupKey(cfg, ticketID)
	ctx := s.ctxOrBackground()

	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" + ticketID
	attempts, timeout := 2, 10*time.Second
	if s.hasTicketBackup(backupKey) {
		attempts, timeout = 1, 6*time.Second
	}
	resp, err := doGetAttempts(ctx, tlsutil.NewHTTPClient(timeout), attempts, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		if cached, ok := s.readCachedTicketDetail(backupKey); ok {
			s.markUnreachable()
			s.supportLogf("servidor inacessivel (%v) — usando detalhe local do chamado %s", err, ticketID)
			return cached, nil
		}
		return APITicket{}, fmt.Errorf("falha ao buscar ticket: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			if cached, ok := s.readCachedTicketDetail(backupKey); ok {
				s.markUnreachable()
				s.supportLogf("HTTP %s — usando detalhe local do chamado %s", resp.Status, ticketID)
				return cached, nil
			}
		}
		return APITicket{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var ticket APITicket
	if err := json.Unmarshal(body, &ticket); err != nil {
		var envelope struct {
			Ticket *APITicket `json:"ticket"`
			Data   *APITicket `json:"data"`
			Item   *APITicket `json:"item"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			switch {
			case envelope.Ticket != nil:
				ticket = *envelope.Ticket
			case envelope.Data != nil:
				ticket = *envelope.Data
			case envelope.Item != nil:
				ticket = *envelope.Item
			default:
				return APITicket{}, fmt.Errorf("resposta inválida: ticket não encontrado no payload")
			}
		} else {
			return APITicket{}, fmt.Errorf("resposta inválida: %w", err)
		}
	}

	s.markReachable()
	if strings.TrimSpace(ticket.ID) != "" {
		s.saveTicketBackup(backupKey, ticket)
	}

	return ticket, nil
}

func parseWorkflowStatesFromBody(body []byte) ([]APIWorkflowState, error) {
	var states []APIWorkflowState
	if err := json.Unmarshal(body, &states); err == nil {
		return states, nil
	}

	var envelope struct {
		Items []APIWorkflowState `json:"items"`
		Data  []APIWorkflowState `json:"data"`
		State []APIWorkflowState `json:"states"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	switch {
	case envelope.Items != nil:
		return envelope.Items, nil
	case envelope.Data != nil:
		return envelope.Data, nil
	case envelope.State != nil:
		return envelope.State, nil
	default:
		return []APIWorkflowState{}, nil
	}
}

// GetTicketWorkflowStates returns available workflow states for tickets.
func (s *Service) GetTicketWorkflowStates() ([]APIWorkflowState, error) {
	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	// Caminho único suportado pelo endpoint do agent.
	target := apiScheme(cfg) + "://" + strings.TrimSpace(cfg.ApiServer) + "/api/v1/agent-auth/me/tickets/workflow-states"
	resp, err := doGetWithRetry(ctx, tlsutil.NewHTTPClient(10*time.Second), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("falha ao buscar estados de workflow: %w", err)
	}

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	states, err := parseWorkflowStatesFromBody(body)
	if err != nil {
		return nil, fmt.Errorf("resposta inválida de estados de workflow: %w", err)
	}
	if states == nil {
		states = []APIWorkflowState{}
	}

	sort.SliceStable(states, func(i, j int) bool {
		if states[i].DisplayOrder == states[j].DisplayOrder {
			return strings.ToLower(states[i].Name) < strings.ToLower(states[j].Name)
		}
		return states[i].DisplayOrder < states[j].DisplayOrder
	})

	s.supportLogf("workflow states carregados: %d estado(s)", len(states))
	return states, nil
}

// GetTicketComments returns comments for a given ticket.
func (s *Service) GetTicketComments(ticketID string) ([]TicketComment, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return nil, fmt.Errorf("ticketId inválido")
	}
	cfg := s.debugConfig()
	backupKey := ticketCommentsBackupKey(cfg, ticketID)
	ctx := s.ctxOrBackground()

	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" + ticketID + "/comments"
	attempts, timeout := 2, 10*time.Second
	if s.hasTicketBackup(backupKey) {
		attempts, timeout = 1, 6*time.Second
	}
	resp, err := doGetAttempts(ctx, tlsutil.NewHTTPClient(timeout), attempts, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		if cached, ok := s.readCachedTicketComments(backupKey); ok {
			s.markUnreachable()
			s.supportLogf("servidor inacessivel (%v) — usando comentários locais do chamado %s", err, ticketID)
			return cached, nil
		}
		return nil, fmt.Errorf("falha ao buscar comentários: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			if cached, ok := s.readCachedTicketComments(backupKey); ok {
				s.markUnreachable()
				s.supportLogf("HTTP %s — usando comentários locais do chamado %s", resp.Status, ticketID)
				return cached, nil
			}
		}
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var comments []TicketComment
	if err := json.Unmarshal(body, &comments); err != nil {
		var envelope struct {
			Items []TicketComment `json:"items"`
			Data  []TicketComment `json:"data"`
		}
		if err2 := json.Unmarshal(body, &envelope); err2 == nil {
			if envelope.Items != nil {
				comments = envelope.Items
			} else {
				comments = envelope.Data
			}
		} else {
			return nil, fmt.Errorf("resposta inválida: %w", err)
		}
	}
	// Opção de produto (a): notas internas não vazam para o agente.
	visible := make([]TicketComment, 0, len(comments))
	for _, c := range comments {
		if c.IsInternal {
			continue
		}
		visible = append(visible, c)
	}
	s.markReachable()
	s.saveTicketBackup(backupKey, visible)
	return visible, nil
}

// AddTicketComment adds a PUBLIC comment and returns the created one.
// Notas internas são exclusivas do portal (opção de produto a).
func (s *Service) AddTicketCommentWithOptions(ticketID, content string) (TicketComment, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return TicketComment{}, fmt.Errorf("ticketId inválido")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return TicketComment{}, fmt.Errorf("content não pode ser vazio")
	}
	if err := s.ensureOnline("adicionar comentário"); err != nil {
		s.supportLogf("comentário bloqueado: %v", err)
		return TicketComment{}, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	payload := map[string]any{
		"content": content,
	}
	body, _ := json.Marshal(payload)

	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" + ticketID + "/comments"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return TicketComment{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", newIdempotencyKey())
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return TicketComment{}, err
	}

	resp, err := doPostWithRetry(ctx, tlsutil.NewHTTPClient(10*time.Second), req)
	if err != nil {
		return TicketComment{}, s.transportError("enviar comentário", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TicketComment{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	s.markReachable()
	var created TicketComment
	if len(respBody) == 0 {
		return created, nil
	}
	if err := json.Unmarshal(respBody, &created); err != nil {
		return TicketComment{}, fmt.Errorf("resposta inválida ao criar comentário: %w", err)
	}
	return created, nil
}

// AddTicketComment adds a comment to a ticket.
func (s *Service) AddTicketComment(ticketID, author, content string) error {
	_ = author
	_, err := s.AddTicketCommentWithOptions(ticketID, content)
	if err != nil {
		return err
	}
	return nil
}

// CloseSupportTicket closes a ticket with optional rating/comment/final workflow state.
func (s *Service) CloseSupportTicket(ticketID string, input CloseTicketInput) (APITicket, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return APITicket{}, fmt.Errorf("ticketId inválido")
	}

	workflowStateID := strings.TrimSpace(input.WorkflowStateID)
	if workflowStateID != "" && !guidPattern.MatchString(workflowStateID) {
		return APITicket{}, fmt.Errorf("workflowStateId inválido")
	}

	if input.Rating != nil {
		if *input.Rating < 1 || *input.Rating > 5 {
			return APITicket{}, fmt.Errorf("rating inválido: informe valor entre 1 e 5")
		}
	}
	if err := s.ensureOnline("fechar chamado"); err != nil {
		s.supportLogf("fechamento bloqueado: %v", err)
		return APITicket{}, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	payload := map[string]any{}
	// 0 = "sem avaliação": o backend valida a nota no intervalo 1..5, então
	// só enviamos o campo quando há nota real (evita 422/erro de validação).
	if input.Rating != nil && *input.Rating > 0 {
		payload["rating"] = *input.Rating
	}
	if c := strings.TrimSpace(input.Comment); c != "" {
		// O backend (CloseAndRateMyTicketCommand) lê o campo "feedback";
		// "comment" era ignorado e a avaliação perdia a observação.
		payload["feedback"] = c
	}
	if workflowStateID != "" {
		payload["workflowStateId"] = workflowStateID
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return APITicket{}, fmt.Errorf("erro ao serializar payload de fechamento: %w", err)
	}

	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" + ticketID + "/close"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return APITicket{}, fmt.Errorf("URL inválida: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", newIdempotencyKey())
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return APITicket{}, err
	}

	s.supportLogf("fechando chamado %s", ticketID)
	resp, err := doPostWithRetry(ctx, tlsutil.NewHTTPClient(15*time.Second), req)
	if err != nil {
		return APITicket{}, s.transportError("fechar chamado", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APITicket{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	// O /close devolve um resumo ({ticketId, closed, workflowStateId, rating}),
	// não a entidade completa. Decodificar isso como APITicket gerava um ticket
	// vazio (id "") e a UI quebrava ao renderizar o detalhe. Reler sempre os
	// detalhes garante closedAt/rating/workflowStateId atualizados.
	s.supportLogf("chamado %s fechado; buscando detalhes atualizados (%d bytes de resposta)", ticketID, len(respBody))
	ticket, err := s.GetSupportTicketDetails(ticketID)
	if err != nil {
		return APITicket{}, err
	}
	s.supportLogf("chamado fechado com sucesso: ticketId=%s", ticket.ID)
	return ticket, nil
}

// ReopenSupportTicket reabre um chamado encerrado do agent e devolve o ticket
// atualizado. A API valida que o chamado pertence ao agent, volta ao estado
// inicial, limpa ClosedAt e descarta a avaliação anterior (reabrir invalida o
// CSAT do fechamento antigo).
func (s *Service) ReopenSupportTicket(ticketID, reason string) (APITicket, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return APITicket{}, fmt.Errorf("ticketId inválido")
	}
	if err := s.ensureOnline("reabrir chamado"); err != nil {
		s.supportLogf("reabertura bloqueada: %v", err)
		return APITicket{}, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	payload := map[string]any{}
	if r := strings.TrimSpace(reason); r != "" {
		// Motivo vai para o activity log da reabertura.
		payload["reason"] = r
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return APITicket{}, fmt.Errorf("erro ao serializar payload de reabertura: %w", err)
	}

	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" + ticketID + "/reopen"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return APITicket{}, fmt.Errorf("URL inválida: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", newIdempotencyKey())
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return APITicket{}, err
	}

	s.supportLogf("reabrindo chamado %s", ticketID)
	resp, err := doPostWithRetry(ctx, tlsutil.NewHTTPClient(15*time.Second), req)
	if err != nil {
		return APITicket{}, s.transportError("reabrir chamado", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APITicket{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	ticket, err := s.GetSupportTicketDetails(ticketID)
	if err != nil {
		return APITicket{}, err
	}
	s.supportLogf("chamado reaberto com sucesso: ticketId=%s", ticket.ID)
	return ticket, nil
}

// RateSupportTicket envia a avaliação (CSAT 1..5) e o feedback de um chamado
// encerrado, devolvendo o ticket atualizado.
func (s *Service) RateSupportTicket(ticketID string, rating int, feedback string) (APITicket, error) {
	ticketID = strings.TrimSpace(ticketID)
	if !guidPattern.MatchString(ticketID) {
		return APITicket{}, fmt.Errorf("ticketId inválido")
	}
	if rating < 1 || rating > 5 {
		return APITicket{}, fmt.Errorf("rating inválido: informe valor entre 1 e 5")
	}
	if err := s.ensureOnline("avaliar chamado"); err != nil {
		s.supportLogf("avaliação bloqueada: %v", err)
		return APITicket{}, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()

	payload := map[string]any{"rating": rating}
	if f := strings.TrimSpace(feedback); f != "" {
		payload["feedback"] = f
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return APITicket{}, fmt.Errorf("erro ao serializar payload de avaliação: %w", err)
	}

	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/tickets/" + ticketID + "/rating"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return APITicket{}, fmt.Errorf("URL inválida: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", newIdempotencyKey())
	if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, cfg.AgentID); err != nil {
		return APITicket{}, err
	}

	s.supportLogf("avaliando chamado %s (rating=%d)", ticketID, rating)
	resp, err := doPostWithRetry(ctx, tlsutil.NewHTTPClient(15*time.Second), req)
	if err != nil {
		return APITicket{}, s.transportError("enviar avaliação", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return APITicket{}, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	ticket, err := s.GetSupportTicketDetails(ticketID)
	if err != nil {
		return APITicket{}, err
	}
	s.supportLogf("avaliação registrada: ticketId=%s rating=%d", ticket.ID, rating)
	return ticket, nil
}

// CloseAgentTicket closes an agent ticket via MCP tool.
func (s *Service) CloseAgentTicket(ticketID string, rating *int, comment, workflowStateID string) (json.RawMessage, error) {
	ticket, err := s.CloseSupportTicket(ticketID, CloseTicketInput{
		Rating:          rating,
		Comment:         comment,
		WorkflowStateID: workflowStateID,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// GetAgentInfoJSON returns the agent info as JSON (for MCP tools).
func (s *Service) GetAgentInfoJSON() (json.RawMessage, error) {
	info, err := s.fetchAgentContext()
	if err != nil {
		return nil, err
	}
	return json.Marshal(info)
}

// maxListedTickets limita quantos chamados a tool list_tickets devolve a IA.
// Sem o limite, um histórico grande de chamados encerrados enchia o resultado
// (o loop de chat trunca tool results em 16 KB) e a verificação de duplicidade
// podia não enxergar o chamado aberto relevante.
const maxListedTickets = 30

// ticketListDescriptionChars limita a descrição resumida de cada chamado.
const ticketListDescriptionChars = 160

// ticketListSummary é a projeção enxuta de um chamado devolvida por
// list_tickets. Campos pesados (submissionSnapshotMarkdown, rating, ids de
// escopo) ficam de fora: o detalhe completo está em get_ticket_details.
type ticketListSummary struct {
	ID              string         `json:"id"`
	Title           string         `json:"title"`
	Description     string         `json:"description,omitempty"`
	Priority        TicketPriority `json:"priority"`
	Category        *string        `json:"category,omitempty"`
	WorkflowStateID string         `json:"workflowStateId,omitempty"`
	IsOpen          bool           `json:"isOpen"`
	CreatedAt       string         `json:"createdAt"`
	ClosedAt        *string        `json:"closedAt,omitempty"`
}

// compactTicketList devolve os chamados ABERTOS primeiro (criados mais
// recentes primeiro dentro de cada grupo), com descrição resumida e limite de
// itens. `total`, `openCount`, `returned` e `truncated` permitem à IA saber se
// o recorte ficou incompleto — sem isso ela poderia concluir, errado, que não
// existe chamado aberto sobre o assunto.
func compactTicketList(tickets []APITicket) map[string]any {
	open := make([]APITicket, 0, len(tickets))
	closed := make([]APITicket, 0, len(tickets))
	for _, t := range tickets {
		if ticketIsOpen(t) {
			open = append(open, t)
		} else {
			closed = append(closed, t)
		}
	}

	ordered := make([]APITicket, 0, len(tickets))
	ordered = append(ordered, open...)
	ordered = append(ordered, closed...)

	returned := len(ordered)
	if returned > maxListedTickets {
		returned = maxListedTickets
	}
	summaries := make([]ticketListSummary, 0, returned)
	for _, t := range ordered[:returned] {
		desc := strings.TrimSpace(t.Description)
		if r := []rune(desc); len(r) > ticketListDescriptionChars {
			desc = string(r[:ticketListDescriptionChars]) + "…"
		}
		summaries = append(summaries, ticketListSummary{
			ID:              t.ID,
			Title:           t.Title,
			Description:     desc,
			Priority:        t.Priority,
			Category:        t.Category,
			WorkflowStateID: t.WorkflowStateID,
			IsOpen:          ticketIsOpen(t),
			CreatedAt:       t.CreatedAt,
			ClosedAt:        t.ClosedAt,
		})
	}

	return map[string]any{
		"total":     len(ordered),
		"openCount": len(open),
		"returned":  len(summaries),
		"truncated": len(ordered) > len(summaries),
		"tickets":   summaries,
	}
}

// ticketIsOpen define "chamado aberto" = sem data de encerramento (ClosedAt).
// Mesma regra ensinada no prompt da IA; centralizada aqui para não divergir.
func ticketIsOpen(t APITicket) bool {
	return t.ClosedAt == nil || strings.TrimSpace(*t.ClosedAt) == ""
}

// ListAgentTickets returns agent tickets as JSON (for MCP tools).
func (s *Service) ListAgentTickets() (json.RawMessage, error) {
	result, err := s.GetSupportTicketList()
	if err != nil {
		return nil, err
	}
	// Resultado resumido e com abertos primeiro: o mesmo payload alimenta a
	// verificação de chamado duplicado feita pela IA antes de create_ticket.
	out := compactTicketList(result.Tickets)
	// `stale` avisa a IA que a lista veio do snapshot offline: dados antigos
	// não devem ser tratados como verdade absoluta na checagem de duplicidade.
	out["stale"] = result.Stale
	return json.Marshal(out)
}

// GetAgentTicketDetails returns one agent ticket as JSON (for MCP tools).
func (s *Service) GetAgentTicketDetails(ticketID string) (json.RawMessage, error) {
	ticket, err := s.GetSupportTicketDetails(ticketID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// AddAgentTicketComment adds a comment to an agent ticket via MCP tool.
func (s *Service) AddAgentTicketComment(ticketID, content string) (json.RawMessage, error) {
	comment, err := s.AddTicketCommentWithOptions(ticketID, content)
	if err != nil {
		return nil, err
	}
	return json.Marshal(comment)
}

// CreateAgentTicket creates a ticket via MCP tool. templateID e os campos
// personalizados são opcionais (abertura normal continua funcionando).
func (s *Service) CreateAgentTicket(title, description string, priority int, category, templateID, departmentID, customFieldsJSON, templateAnswersJSON string) (json.RawMessage, error) {
	input := CreateTicketInput{
		Title:       title,
		Description: description,
		Priority:    priority,
		Category:    category,
		TemplateID:  templateID,
		// Departamento é obrigatório na API: define responsável (auto-atribuição)
		// e perfil/SLA do chamado.
		DepartmentID: departmentID,
	}
	if strings.TrimSpace(customFieldsJSON) != "" {
		fields := map[string]any{}
		if err := json.Unmarshal([]byte(customFieldsJSON), &fields); err != nil {
			return nil, fmt.Errorf("customFields inválido: %w", err)
		}
		input.CustomFields = fields
	}
	if strings.TrimSpace(templateAnswersJSON) != "" {
		answers := map[string]any{}
		if err := json.Unmarshal([]byte(templateAnswersJSON), &answers); err != nil {
			return nil, fmt.Errorf("templateAnswers inválido: %w", err)
		}
		input.TemplateAnswers = answers
	}
	ticket, err := s.CreateSupportTicket(input)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ticket)
}

// ListAgentTicketTemplates returns the ticket templates available to this agent.
func (s *Service) ListAgentTicketTemplates() (json.RawMessage, error) {
	if !s.featureEnabled(s.supportEnabled()) {
		s.supportLogf("suporte desabilitado pela configuração do agente")
		return json.Marshal([]TicketTemplateOption{})
	}

	info, err := s.fetchAgentContext()
	if err != nil {
		return nil, err
	}

	cfg := s.debugConfig()
	ctx := s.ctxOrBackground()
	target := apiScheme(cfg) + "://" + cfg.ApiServer + "/api/v1/agent-auth/me/ticket-templates"
	resp, err := doGetWithRetry(ctx, tlsutil.NewHTTPClient(10*time.Second), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := netutil.SetAgentAuthHeadersWithAgentID(req, cfg.AuthToken, info.AgentID); err != nil {
			return nil, err
		}
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("falha ao buscar templates de chamado: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var templates []TicketTemplateOption
	if err := json.Unmarshal(body, &templates); err != nil {
		return nil, fmt.Errorf("resposta inválida ao listar templates: %w", err)
	}
	if templates == nil {
		templates = []TicketTemplateOption{}
	}
	return json.Marshal(templates)
}

// extractStr delega para a implementação canônica do supportmeta.
func extractStr(raw map[string]any, key string) string {
	return supportmeta.ExtractStr(raw, key)
}

func (s *Service) ctxOrBackground() context.Context {
	if ctx := s.ctx(); ctx != nil {
		return ctx
	}
	return context.Background()
}
