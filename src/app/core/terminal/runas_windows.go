//go:build windows

package terminal

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ── Run-As do terminal (SYSTEM ↔ usuário) ──
//
// O shell do terminal remoto pode rodar em duas identidades:
//   - RunAsSystem / RunAsInherit: herda a identidade do processo do agente
//     (SYSTEM quando o agent-service roda como serviço; o usuário logado no
//     modo GUI).
//   - RunAsUser: o shell roda na identidade do usuário da SESSÃO DE CONSOLE
//     ATIVA (o mesmo token que o worker de sessão remota usa), obtido via
//     WTSGetActiveConsoleSessionId → WTSQueryUserToken → DuplicateTokenEx
//     (TokenPrimary). Sem usuário logado o pedido faz FALLBACK para a
//     identidade herdada (nunca falha a sessão de terminal).
//
// Segurança: o token NUNCA vem do servidor/viewer — só o token primário da
// sessão de console local. Não há escalada de privilégio: o operador do RMM
// recebe no máximo o poder do usuário logado.

type RunAs string

const (
	// RunAsInherit ("") — comportamento atual: herda a identidade do agente.
	RunAsInherit RunAs = ""
	// RunAsSystem — idêntico ao inherit (não passa token alternativo).
	RunAsSystem RunAs = "system"
	// RunAsUser — token primário da sessão de console ativa.
	RunAsUser RunAs = "user"
)

// ParseRunAs converte a string recebida do servidor (session.Meta["runAs"])
// em RunAs. Valores inválidos são mapeados para RunAsInherit (o caller pode
// registrar aviso com ValidRunAs).
func ParseRunAs(s string) RunAs {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "system":
		return RunAsSystem
	case "user":
		return RunAsUser
	default:
		return RunAsInherit // "", "inherit", desconhecidos
	}
}

// ValidRunAs reporta se a string é um valor reconhecido de runAs (usado pelo
// caller para logar valor inválido recebido do servidor em vez de silenciar).
func ValidRunAs(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "inherit", "system", "user":
		return true
	}
	return false
}

const (
	tokenPrimary          = 1 // TokenType
	securityImpersonation = 2 // SECURITY_IMPERSONATION
	maximumAllowed        = 0x02000000
)

var (
	wtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")
	advapi32 = windows.NewLazySystemDLL("advapi32.dll")
	userenv  = windows.NewLazySystemDLL("userenv.dll")

	procWTSQueryUserToken       = wtsapi32.NewProc("WTSQueryUserToken")
	procDuplicateTokenEx        = advapi32.NewProc("DuplicateTokenEx")
	procCreateEnvironmentBlock  = userenv.NewProc("CreateEnvironmentBlock")
	procDestroyEnvironmentBlock = userenv.NewProc("DestroyEnvironmentBlock")
)

// resolveUserTokenFn é injetável em teste (o token real exige sessão de
// console ativa — indisponível no CI).
var resolveUserTokenFn = resolveUserToken

// userTokenCache guarda o token primário da sessão de console por um TTL
// curto: cada retry do ConPTY (até 8 no startup por morte prematura) NÃO deve
// reabrir WTSQueryUserToken/DuplicateTokenEx. O handle do cache permanece
// aberto — o token é o DONO do cache; o shell usa o handle apenas até o
// CreateProcessW (o kernel duplica no processo filho), então o TTL curto é
// seguro e evita handle churn.
type userTokenCacheEntry struct {
	tok     windows.Token
	session uint32
	ident   string
	expires time.Time
}

var (
	userTokenMu    sync.Mutex
	userTokenCache *userTokenCacheEntry
)

const userTokenCacheTTL = 30 * time.Second

// runAsResolution é o resultado da resolução de identidade para uma sessão.
type runAsResolution struct {
	tok            windows.Token // 0 = herdado (SYSTEM/usuário do agente)
	closeTok       func()        // fecha o token (não-nil quando tok != 0)
	identity       string        // "user <domain\name>" ou "inherit"
	effectiveRunAs string        // "user" ou "system"/"inherit" (term.ready)
}

// resolveRunAs traduz o pedido em token. Falha do RunAsUser degrada para
// herdado com log de aviso (nunca erro: o terminal não pode deixar de abrir).
func resolveRunAs(ra RunAs) runAsResolution {
	if ra != RunAsUser {
		return runAsResolution{identity: "inherit", effectiveRunAs: EffectiveRunAs(ra)}
	}
	tok, session, ident, err := cachedUserToken()
	if err != nil || tok == 0 {
		log.Printf("[terminal] runAs=user sem usuário logado (console session=%d): %v — usando identidade herdada", session, err)
		return runAsResolution{identity: "inherit", effectiveRunAs: EffectiveRunAs(RunAsInherit)}
	}
	// closeTok fica nil: o CACHE é o dono do handle (o shell usa o token
	// apenas até o CreateProcessW — o kernel duplica no processo filho).
	return runAsResolution{
		tok:            tok,
		closeTok:       nil,
		identity:       ident,
		effectiveRunAs: "user",
	}
}

// cachedUserToken devolve o token primário da sessão de console ativa com
// cache TTL curto. O CALLER NÃO fecha o handle retornado — o cache é o dono.
func cachedUserToken() (windows.Token, uint32, string, error) {
	userTokenMu.Lock()
	defer userTokenMu.Unlock()
	if userTokenCache != nil && time.Now().Before(userTokenCache.expires) {
		c := userTokenCache
		return c.tok, c.session, c.ident, nil
	}
	tok, session, ident, err := resolveUserTokenFn()
	if err != nil || tok == 0 {
		userTokenCache = nil
		return 0, session, "", err
	}
	// O handle anterior (expirado) é fechado aqui — o cache é o único dono.
	if userTokenCache != nil && userTokenCache.tok != 0 {
		_ = userTokenCache.tok.Close()
	}
	userTokenCache = &userTokenCacheEntry{tok: tok, session: session, ident: ident, expires: time.Now().Add(userTokenCacheTTL)}
	return tok, session, ident, nil
}

// resetUserTokenCache invalida o cache (teste/força nova resolução).
func resetUserTokenCache() {
	userTokenMu.Lock()
	userTokenCache = nil
	userTokenMu.Unlock()
}

// resolveUserToken resolve o token primário do usuário da sessão de console
// ativa. Retorna (0, session, "", err) quando não há usuário logado ou o
// processo não tem permissão (WTSQueryUserToken exige SYSTEM/admin).
func resolveUserToken() (windows.Token, uint32, string, error) {
	session := windows.WTSGetActiveConsoleSessionId()
	if session == 0xFFFFFFFF || session == 0 {
		return 0, session, "", errors.New("nenhuma sessao de console ativa")
	}
	var raw uintptr
	r1, _, callErr := procWTSQueryUserToken.Call(
		uintptr(session),
		uintptr(unsafe.Pointer(&raw)),
	)
	if r1 == 0 {
		return 0, session, "", fmt.Errorf("WTSQueryUserToken(%d): %v", session, callErr)
	}
	impersonation := windows.Token(raw)
	defer func() { _ = impersonation.Close() }()

	// CreateProcessW/CreatePseudoConsole exige token PRIMÁRIO — duplica o
	// token de impersonação devolvido pelo WTS em TokenPrimary.
	var primary windows.Token
	r1, _, callErr = procDuplicateTokenEx.Call(
		uintptr(impersonation),
		uintptr(maximumAllowed),
		0, // SecurityAttributes
		uintptr(securityImpersonation),
		uintptr(tokenPrimary),
		uintptr(unsafe.Pointer(&primary)),
	)
	if r1 == 0 || primary == 0 {
		return 0, session, "", fmt.Errorf("DuplicateTokenEx: %v", callErr)
	}

	ident := "user (console session " + fmt.Sprint(session) + ")"
	if name := tokenUserName(primary); name != "" {
		ident = "user " + name
	}
	return primary, session, ident, nil
}

// tokenUserName resolve domain\user do token (auditoria no log).
func tokenUserName(tok windows.Token) string {
	user, err := tok.GetTokenUser()
	if err != nil || user == nil {
		return ""
	}
	domain, acct, _, err := user.User.Sid.LookupAccount("")
	if err != nil {
		return ""
	}
	if domain != "" {
		return domain + "\\" + acct
	}
	return acct
}

// effectiveRunAs devolve a identidade EFETIVA que uma sessão com o pedido
// informado vai usar — campo "runAs" do term.ready (aditivo).
func EffectiveRunAs(ra RunAs) string {
	switch ra {
	case RunAsUser:
		if _, _, _, err := cachedUserToken(); err == nil {
			return "user"
		}
		if isProcessSystem() {
			return "system"
		}
		return "inherit"
	case RunAsSystem:
		return "system"
	default:
		return "inherit"
	}
}

// isProcessSystem reporta se o token do processo atual é LOCAL SYSTEM
// (S-1-5-18) — usado para traduzir fallback de runAs=user em "system".
func isProcessSystem() bool {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		return false
	}
	defer func() { _ = tok.Close() }()
	user, err := tok.GetTokenUser()
	if err != nil || user == nil {
		return false
	}
	return user.User.Sid.String() == "S-1-5-18"
}

// userEnvironmentBlock devolve o bloco de ambiente UTF-16 do usuário (via
// CreateEnvironmentBlock). O bloco é passado como lpEnvironment do
// CreateProcessW e DEVE permanecer válido até a chamada; o caller chama
// destroyEnvironmentBlock com o mesmo ponteiro depois.
//
// Por que é obrigatório com token: um processo criado via CreateProcessAsUser
// herda por padrão o ambiente do SERVIÇO (SYSTEM) — %USERPROFILE%,
// %APPDATA%, %LOCALAPPDATA% apontariam para systemprofile. O
// CreateEnvironmentBlock monta o ambiente real do usuário.
func userEnvironmentBlock(tok windows.Token) (unsafe.Pointer, error) {
	if tok == 0 {
		return nil, errors.New("token vazio")
	}
	var block unsafe.Pointer
	r1, _, callErr := procCreateEnvironmentBlock.Call(
		uintptr(unsafe.Pointer(&block)),
		uintptr(tok),
		0, // bInherit = FALSE — não mescla o ambiente do serviço
	)
	if r1 == 0 || block == nil {
		return nil, fmt.Errorf("CreateEnvironmentBlock: %v", callErr)
	}
	return block, nil
}

// destroyEnvironmentBlock libera o bloco criado por userEnvironmentBlock.
func destroyEnvironmentBlock(block unsafe.Pointer) {
	if block != nil {
		_, _, _ = procDestroyEnvironmentBlock.Call(uintptr(block))
	}
}
