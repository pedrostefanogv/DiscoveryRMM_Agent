//go:build windows

package service

import (
	"fmt"
	"net"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Windows API carregada via LazyDLL (não disponível em golang.org/x/sys/windows v0.43.0)
var (
	modAdvapi32Windows             = windows.NewLazySystemDLL("advapi32.dll")
	procImpersonateNamedPipeClient = modAdvapi32Windows.NewProc("ImpersonateNamedPipeClient")
	modAdvapi32ForLogon            = windows.NewLazySystemDLL("advapi32.dll")
	procImpersonateLoggedOnUser    = modAdvapi32ForLogon.NewProc("ImpersonateLoggedOnUser")
)

// callImpersonateNamedPipeClient invoca ImpersonateNamedPipeClient(hNamedPipe).
// O servidor impersona o cliente para leitura de identidade ou execução de ações.
func callImpersonateNamedPipeClient(pipeHandle windows.Handle) error {
	r1, _, e1 := procImpersonateNamedPipeClient.Call(uintptr(pipeHandle))
	if r1 == 0 {
		if e1 != windows.ERROR_SUCCESS {
			return fmt.Errorf("ImpersonateNamedPipeClient: %w", e1)
		}
		return fmt.Errorf("ImpersonateNamedPipeClient retornou false")
	}
	return nil
}

// CallImpersonateLoggedOnUser invoca ImpersonateLoggedOnUser(hToken).
// Usado na execução de ações para rodar no contexto do usuário.
func callImpersonateLoggedOnUser(token windows.Token) error {
	r1, _, e1 := procImpersonateLoggedOnUser.Call(uintptr(token))
	if r1 == 0 {
		if e1 != windows.ERROR_SUCCESS {
			return fmt.Errorf("ImpersonateLoggedOnUser: %w", e1)
		}
		return fmt.Errorf("ImpersonateLoggedOnUser retornou false")
	}
	return nil
}

// pendingConnTokens armazena tokens por reqID (vida curta: da conexão até promoteTokenForAction).
var pendingConnTokens sync.Map

// pendingActionTokens armazena tokens por actionID (consumidos pelo worker de execução).
var pendingActionTokens sync.Map

// applyServerSideIdentity resolve a identidade real do cliente via ImpersonateNamedPipeClient
// e sobrescreve os campos UserSID/UserName do request com dados verificados pelo servidor.
// Para comandos "execute", captura e armazena um token primário duplicado para uso assíncrono.
// O campo req.UserSID declarado pelo cliente é SEMPRE descartado no Windows.
func applyServerSideIdentity(conn net.Conn, req *ClientRequest) {
	type fder interface{ Fd() uintptr }
	fderConn, ok := conn.(fder)
	if !ok {
		// Named Pipe sem Fd() acessível: limpar identidade declarada pelo cliente.
		req.UserSID = ""
		req.UserName = ""
		return
	}

	handle := windows.Handle(fderConn.Fd())

	if err := callImpersonateNamedPipeClient(handle); err != nil {
		fmt.Printf("[IPC.Identity] ImpersonateNamedPipeClient falhou: %v — identidade não verificada\n", err)
		req.UserSID = ""
		req.UserName = ""
		return
	}
	// OBRIGATÓRIO: reverter impersonação antes de sair desta função.
	defer windows.RevertToSelf() //nolint:errcheck

	// Abrir o token de impersonação do thread atual (agora representa o cliente).
	var impToken windows.Token
	if err := windows.OpenThreadToken(
		windows.CurrentThread(),
		windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE,
		true, // openAsSelf: necessário para evitar problemas de acesso em contexto SYSTEM
		&impToken,
	); err != nil {
		fmt.Printf("[IPC.Identity] OpenThreadToken falhou: %v\n", err)
		req.UserSID = ""
		req.UserName = ""
		return
	}
	defer impToken.Close()

	// Extrair informações do usuário do token.
	user, err := impToken.GetTokenUser()
	if err != nil {
		fmt.Printf("[IPC.Identity] GetTokenUser falhou: %v\n", err)
		req.UserSID = ""
		req.UserName = ""
		return
	}

	// SID verificado pelo servidor (não declarado pelo cliente).
	req.UserSID = user.User.Sid.String()

	// Resolver nome legível. Falha não é fatal (SID já é suficiente para auditoria).
	account, domain, _, lookupErr := user.User.Sid.LookupAccount("")
	if lookupErr == nil && strings.TrimSpace(account) != "" {
		req.UserName = domain + `\` + account
	}
	// Se LookupAccount falhou, mantém UserName declarado pelo cliente (não-crítico).

	// Para comandos execute: duplicar token primário para uso assíncrono pelo worker.
	if strings.EqualFold(req.Command, "execute") {
		var primaryToken windows.Token
		err := windows.DuplicateTokenEx(
			impToken,
			windows.TOKEN_ALL_ACCESS,
			nil,                           // security attributes padrão
			windows.SecurityImpersonation, // nível de impersonação
			windows.TokenPrimary,          // tipo primário para CreateProcessAsUser
			&primaryToken,
		)
		if err != nil {
			fmt.Printf("[IPC.Identity] DuplicateTokenEx falhou: %v — execução ocorrerá como SYSTEM\n", err)
		} else {
			pendingConnTokens.Store(req.ID, primaryToken)
		}
	}
}

// promoteTokenForAction move o token da chave reqID para a chave actionID.
// Deve ser chamado em cmdExecute imediatamente após EnqueueAction ter sucesso.
func promoteTokenForAction(reqID, actionID string) {
	if v, ok := pendingConnTokens.LoadAndDelete(reqID); ok {
		pendingActionTokens.Store(actionID, v)
	}
}

// consumeActionToken retorna e remove o token associado ao actionID.
// O chamador é responsável por fechar o token após o uso.
func consumeActionToken(actionID string) (windows.Token, bool) {
	v, ok := pendingActionTokens.LoadAndDelete(actionID)
	if !ok {
		return 0, false
	}
	tok, ok := v.(windows.Token)
	return tok, ok
}
