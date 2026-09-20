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

	"discovery/app/core/agentconn"
	"discovery/app/core/platform"
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
	// Guard-rail anti-regressão UIPI: a integridade EFETIVA do worker decide
	// se o input chega em janelas elevadas (Gerenciador de Tarefas roda SEMPRE
	// High via autoElevate; a UI do agente é High via requireAdministrator).
	// Um worker Medium perde cliques/teclado enquanto qualquer uma delas está
	// em primeiro plano — e volta ao fechá-las. Esta linha sempre presente
	// torna o problema diagnosticável no agent-service.log com um grep.
	fmt.Fprintf(os.Stderr, "[remote-session-worker] contexto de privilégio: %s\n", platform.ElevationReport())

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

	// Resolução do token EM CADEIA (bug 2026-09-20 — Authorization Violation):
	// 1) authToken injetado no payload pelo serviço (token VIVO em memória —
	//    cobre rotação ainda não persistida);
	// 2) config.json (persistência canônica da rotação P2P/zero-touch);
	// 3) debug_config.json (legado — fica velho após a rotação; mantido como
	//    último recurso para instalações antigas).
	authToken := strings.TrimSpace(stringAuthTokenFromPayload(cmd))
	if authToken == "" {
		authToken = GetInstallerAuthTokenForWorker()
	}
	if authToken == "" {
		authToken = trimSpace(cfg.AuthToken)
	}
	token, err := netutil.NormalizeAgentToken(authToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] token inválido (%v) — fontes: payload/config.json/debug_config.json\n", err)
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

	// Monitor de stdin: EOF (serviço morreu) → cancela sessão; mensagem
	// framed {"action":"stop"} → cancela; linha crua "stop" → cancela.
	//
	// FIX 2026-09-16: o monitor antigo só comparava LINHAS com "stop", mas o
	// serviço envia o stop FRAMED (writeWorkerPayload: 4B len + JSON). A linha
	// nunca casava, o stop remoto nunca cancelava e o serviço esperava o teto
	// de 5s de stopRemoteSessionWorker e matava o processo (Kill) — o
	// encerramento gracioso (flush NATS, Shutdown das sessões) nunca rodava.
	// Evidência: agent-service.log das estações com resultado de
	// remotesessionstop sempre ~5s após o comando.
	go func() {
		br := bufio.NewReader(os.Stdin)
		for {
			line, err := readStopOrCommandLine(br)
			if err != nil {
				cancel() // EOF — serviço encerrou o pipe (ou morreu)
				return
			}
			if line == "stop" {
				cancel()
				return
			}
			var cmd map[string]any
			if json.Unmarshal([]byte(line), &cmd) == nil {
				if act, _ := cmd["action"].(string); act == "stop" {
					cancel()
					return
				}
				// FIX 17/09: comandos framed de runtime (quality,
				// recording_start/stop, monitor...) eram DESCARTADOS aqui — o
				// serviço reusa o worker vivo e escreve o comando no stdin, mas
				// o monitor só entendia stop. Resultado: a troca manual de
				// qualidade nunca chegava à sessão ativa (o card de estatística
				// nunca mudava). Encaminha ao manager do próprio worker.
				if handled, errMsg := mgr.HandleCommand(ctx, cmd); !handled {
					fmt.Fprintf(os.Stderr, "[remote-session-worker] comando não tratado (action=%v): %s\n", cmd["action"], errMsg)
				} else if errMsg != "" {
					fmt.Fprintf(os.Stderr, "[remote-session-worker] comando %v: %s\n", cmd["action"], errMsg)
				}
			}
		}
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

// readStopOrCommandLine lê do stdin do worker aceitando os dois formatos que
// chegam após o payload inicial:
//   - mensagem framed (4B big-endian len + JSON) — caminho do serviço
//     (writeWorkerPayload: stop, quality, monitor de parent.Done);
//   - linha crua terminada em \n — caminho de teste manual ("stop\n").
//
// Retorna o conteúdo (JSON framed ou linha sem \n). Erro apenas quando o
// stdin fecha (EOF) ou falha — nessas condições quem chama cancela a sessão.
func readStopOrCommandLine(br *bufio.Reader) (string, error) {
	head, err := br.Peek(4)
	if err != nil {
		// EOF com menos de 4 bytes: pode ser uma linha curta final ("stop")
		// sem newline — drena o restante antes de propagar o erro.
		if rest, rerr := br.ReadString('\n'); rerr == nil || (rerr == io.EOF && strings.TrimSpace(rest) != "") {
			return strings.TrimSpace(rest), nil
		}
		return "", err
	}
	n := binary.BigEndian.Uint32(head)
	if n > 0 && n <= 1<<20 { // mesmo teto de readFramedStdin
		buf := make([]byte, 4+n)
		if _, err := io.ReadFull(br, buf); err == nil {
			return string(buf[4:]), nil
		}
		// Frame truncado: cai para o modo linha com o que restou.
	}
	line, rerr := br.ReadString('\n')
	if rerr != nil && rerr != io.EOF {
		return "", rerr
	}
	return strings.TrimSpace(line), nil
}

// connectWorkerNATS conecta ao NATS de streaming seguindo a MESMA política do
// core (agentconn runSession): NATIVO primeiro, websocket como fallback.
//
// Ordem dos candidatos (dedupe preserva a primeira ocorrência):
//  1. natsServer configurado (nativo)
//  2. natsWsServer configurado (websocket)
//  3. nats://host:4222 derivado da API (nativo)
//  4. wss://host:443/nats/ derivado da API (websocket)
//
// FIX 2026-09-16 (acesso remoto não conecta nas estações): os candidatos iam
// crus ao nats.Connect. Duas falhas encadeadas deixavam o worker sem stream
// em redes onde a porta 4222 não é alcançável (nas quais o próprio core só
// sustenta o transporte nats-wss):
//  1. candidato wss sem porta explícita ("wss://host/nats/") — o nats.go NÃO
//     aplica porta padrão para ws/wss → "dial tcp host:0" (falha instantânea);
//  2. path do websocket na URL — o dialer do nats.go ignora o path da URL;
//     sem nats.ProxyPath o handshake cai no "/" do site → "invalid websocket
//     connection".
//
// A normalização agora passa por agentconn.NormalizeClientEndpoint (porta
// explícita + ProxyPath). Timeout por scheme: 3s para o nativo — rede saudável
// conecta em <1s e, com a porta dropada por firewall (o caso das estações,
// onde o dial TRAVA em vez de recusar), o failover ao wss acontece em ~3s;
// o viewer aguenta ~31s de reconexão (RECONNECT_DELAYS 1+2+4+8+16s), então um
// nativo lento não é perdido — a próxima sessão tenta nativo de novo. 8s para
// ws/wss (TLS + handshake do proxy). Cada tentativa é logada no stderr
// (drenado pelo serviço para o agent-service.log).
func connectWorkerNATS(cfg WorkerDebugConfig, agentID, token string) (*nats.Conn, error) {
	var candidates []string
	if nat := trimSpace(cfg.NatsServer); nat != "" {
		candidates = append(candidates, nat)
	}
	if wss := trimSpace(cfg.NatsWsServer); wss != "" {
		candidates = append(candidates, wss)
	}
	if host := extractAPIHost(cfg.ApiServer); host != "" {
		candidates = append(candidates, "nats://"+host+":4222")
	}
	if host := extractAPIHost(cfg.ApiServer); host != "" {
		candidates = append(candidates, "wss://"+host+"/nats/")
	}

	type attempt struct {
		url       string
		proxyPath string
		timeout   time.Duration
	}
	const (
		nativeDialTimeout = 3 * time.Second // nativo: failover rápido ao wss (dial dropado trava)
		wsDialTimeout     = 8 * time.Second // ws/wss: TLS + handshake do proxy
	)
	var attempts []attempt
	seen := map[string]struct{}{}
	for _, raw := range candidates {
		ep, err := agentconn.NormalizeClientEndpoint(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[remote-session-worker] endpoint NATS ignorado (%q): %v\n", raw, err)
			continue
		}
		if _, dup := seen[ep.URL]; dup {
			continue
		}
		seen[ep.URL] = struct{}{}
		timeout := wsDialTimeout
		if strings.HasPrefix(ep.URL, "nats://") {
			timeout = nativeDialTimeout
		}
		attempts = append(attempts, attempt{url: ep.URL, proxyPath: ep.ProxyPath, timeout: timeout})
	}
	if len(attempts) == 0 {
		return nil, fmt.Errorf("nenhum endpoint NATS configurado")
	}

	var lastErr error
	for _, a := range attempts {
		fmt.Fprintf(os.Stderr, "[remote-session-worker] tentando NATS %s (timeout %s)\n", a.url, a.timeout)
		opts := []nats.Option{
			nats.Name("discovery-rs-worker-" + agentID),
			nats.Token(token),
			nats.Timeout(a.timeout),
			nats.ReconnectWait(10 * time.Second),
			nats.MaxReconnects(-1),
		}
		if a.proxyPath != "" {
			opts = append(opts, nats.ProxyPath(a.proxyPath))
		}
		nc, err := nats.Connect(a.url, opts...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[remote-session-worker] NATS falhou (%s): %v\n", a.url, err)
			lastErr = err
			continue
		}
		fmt.Fprintf(os.Stderr, "[remote-session-worker] nats conectado: %s\n", a.url)
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
