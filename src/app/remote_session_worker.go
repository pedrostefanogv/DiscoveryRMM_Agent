//go:build windows

package app

// Worker de remote session (PLANO_AGENT_SERVICE_SYSTEM.md §7.2, base MeshAgent).
//
// O serviço SYSTEM (sessão 0) não tem desktop interativo: captura não produz
// frames e SendInput é bloqueado por UIPI. Quando não há UI companion conectada
// (ex.: tela de logon, usuário não logado), o serviço spawn este binário com
// --remote-session-worker na sessão interativa (CreateProcessAsUser) ou no
// winsta0\winlogon. O worker:
//   1. Lê o payload do comando (JSON) via stdin (framed: 4B len + data) —
//      evita expor o token na linha de comando.
//   2. Abre conexão NATS dedicada de streaming (mesma lógica do companion).
//   3. Executa o comando no remoteSessionMgr local.
//   4. Bloqueia até a sessão encerrar (stop via stdin "stop" ou expiração).

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"discovery/app/core/remotesession"
	"discovery/app/netutil"
)

// RunRemoteSessionWorker é o entrypoint do modo worker, chamado pelo main.go
// quando o binário é lançado com --remote-session-worker. Bloqueia até a
// sessão encerrar ou o stdin fechar (serviço morreu).
func RunRemoteSessionWorker() {
	// Payload do comando chega via stdin framed (4B big-endian len + JSON).
	// Isso evita passar o payload (que pode conter dados sensíveis) na linha
	// de comando, visível no Task Manager/WMI.
	payload, err := readFramedStdin(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] falha ao ler payload: %v\n", err)
		os.Exit(2)
	}
	var cmd map[string]any
	if err := json.Unmarshal(payload, &cmd); err != nil {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] payload inválido: %v\n", err)
		os.Exit(2)
	}

	sessionID, _ := cmd["sessionId"].(string)
	fmt.Fprintf(os.Stderr, "[remote-session-worker] iniciando sessão %s\n", sessionID)

	// ── Config (mesma leitura do config de produção do agente) ──
	cfg := loadWorkerDebugConfig()
	agentCfg := loadWorkerAgentConfig()

	agentID := trimSpace(cfg.AgentID)
	clientID := trimSpace(agentCfg.ClientID)
	siteID := trimSpace(agentCfg.SiteID)
	if agentID == "" || clientID == "" || siteID == "" {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] identidade incompleta (agentId/clientId/siteId)\n")
		os.Exit(3)
	}

	token, err := netutil.NormalizeAgentToken(cfg.AuthToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] token inválido: %v\n", err)
		os.Exit(3)
	}

	nc, err := connectWorkerNATS(cfg, agentID, token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] NATS indisponível: %v\n", err)
		os.Exit(4)
	}
	defer nc.Close()

	mgr := remotesession.NewManager(nil)
	mgr.SetNatsConn(nc, clientID, siteID, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Monitor de stdin: EOF (serviço morreu) ou "stop" → cancela sessão.
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if scanner.Text() == "stop" {
				cancel()
				return
			}
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "[remote-session-worker] stdin interrompido: %v\n", err)
		}
		cancel() // EOF — serviço encerrou o pipe
	}()

	// Executa o comando (start/stop/quality).
	handled, errMsg := mgr.HandleCommand(ctx, cmd)
	if !handled {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] comando não tratado: %v\n", errMsg)
		os.Exit(5)
	}
	if errMsg != "" {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] mensagem: %s\n", errMsg)
	}

	// Bloqueia até encerrar. Três condições de saída:
	//   1. stop via stdin ("stop" ou EOF — serviço mandou parar/morreu)
	//   2. contexto cancelado
	//   3. TODAS as sessões do manager fecharam (expiração, viewer fechou,
	//      terminal exit) — sem isso o worker ficaria vivo para sempre como
	//      processo órfão na sessão do usuário.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
		case <-ticker.C:
			if mgr.CountActive() == 0 {
				fmt.Fprintf(os.Stderr, "[remote-session-worker] nenhuma sessão ativa — encerrando\n")
			}
			if mgr.CountActive() == 0 {
				cancel()
			}
		}
		if ctx.Err() != nil {
			break
		}
	}

	// Encerra sessões ativas graciosamente.
	_ = mgr.Shutdown()
	fmt.Fprintf(os.Stderr, "[remote-session-worker] encerrado\n")
}

// readFramedStdin lê uma mensagem framed (4B big-endian length + payload).
func readFramedStdin(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("lendo tamanho: %w", err)
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > 1<<20 { // 1MB de teto
		return nil, fmt.Errorf("tamanho de payload inválido: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("lendo payload: %w", err)
	}
	return buf, nil
}

// connectWorkerNATS conecta ao NATS de streaming (wss → nats → derivado da API).
func connectWorkerNATS(cfg WorkerDebugConfig, agentID, token string) (*nats.Conn, error) {
	var candidates []string
	if wss := trimSpace(cfg.NatsWsServer); wss != "" {
		candidates = append(candidates, wss)
	}
	if nat := trimSpace(cfg.NatsServer); nat != "" {
		candidates = append(candidates, nat)
	}
	if host := extractAPIHost(cfg.ApiServer); host != "" {
		candidates = append(candidates,
			"wss://"+host+"/nats/",
			"nats://"+host+":4222",
		)
	}
	var lastErr error
	for _, server := range candidates {
		nc, err := nats.Connect(server,
			nats.Name("discovery-rs-worker-"+agentID),
			nats.Token(token),
			nats.Timeout(8*time.Second),
			nats.ReconnectWait(10*time.Second),
			nats.MaxReconnects(-1),
		)
		if err != nil {
			lastErr = err
			continue
		}
		return nc, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("nenhum endpoint NATS disponível: %w", lastErr)
	}
	return nil, fmt.Errorf("nenhum endpoint NATS configurado")
}

// trimSpace delega a strings.TrimSpace (trata todos os whitespace, não só
// espaço/tab — payloads JSON podem conter \r\n residual).
func trimSpace(s string) string {
	return strings.TrimSpace(s)
}

// loadWorkerDebugConfig / loadWorkerAgentConfig: bridges para as mesmas
// funções usadas pela App (config de produção em C:\ProgramData\Discovery).
func loadWorkerDebugConfig() WorkerDebugConfig {
	cfg := GetDebugConfigForWorker()
	return WorkerDebugConfig{
		ApiServer:    cfg.ApiServer,
		NatsServer:   cfg.NatsServer,
		NatsWsServer: cfg.NatsWsServer,
		AuthToken:    cfg.AuthToken,
		AgentID:      cfg.AgentID,
	}
}

func loadWorkerAgentConfig() WorkerAgentConfig {
	cfg := GetAgentConfigurationForWorker()
	return WorkerAgentConfig{ClientID: cfg.ClientID, SiteID: cfg.SiteID}
}

// WorkerDebugConfig/WorkerAgentConfig: structs simples para o worker (evitam
// importar os tipos completos dos services no main).
type WorkerDebugConfig struct {
	ApiServer    string
	NatsServer   string
	NatsWsServer string
	AuthToken    string
	AgentID      string
}

type WorkerAgentConfig struct {
	ClientID string
	SiteID   string
}

// GetDebugConfigForWorker / GetAgentConfigurationForWorker são implementados
// em remote_session_worker_config.go (leitura direta do config persistido,
// sem depender da App inicializada).
