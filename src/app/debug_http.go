//go:build windows

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"discovery/app/debughttp"
)

// PublishChatEvent publica um evento de chat para os inscritos SSE.
// Chamado por StartChatStream para forward dos eventos Wails → HTTP.
func (a *App) PublishChatEvent(eventType, data string) {
	if a.chatEvents != nil {
		a.chatEvents.Publish(eventType, data)
	}
}

// SetDebugFrontendAssets stores the embedded frontend filesystem for the debug HTTP server.
// The embed.FS from `//go:embed all:frontend` has a `frontend/` prefix — we strip it
// via fs.Sub so that the HTTP server can serve paths as `index.html`, `app.js`, etc.
// Must be called before App.startup() runs (i.e. before wails.Run).
func SetDebugFrontendAssets(fs http.FileSystem) {
	debughttp.SetFrontendAssets(fs)
}

// startDebugHTTPInternal binds and starts the HTTP server on the given bind address.
// If port is 0, a random port is allocated. Otherwise the specified port is used.
func (a *App) startDebugHTTPInternal(bindAddr string, port int) error {
	fs := debughttp.GetFrontendFS()
	if fs == nil {
		return fmt.Errorf("frontend assets não configurados — chame SetDebugFrontendAssets antes")
	}

	listenAddr := fmt.Sprintf("%s:%d", bindAddr, port)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("falha ao criar listener debug-http em %s: %w", listenAddr, err)
	}

	port = listener.Addr().(*net.TCPAddr).Port
	bindAll := bindAddr == "0.0.0.0"

	mux := http.NewServeMux()

	// Static file serving from embedded FS (SPA fallback to index.html)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// API routes are handled separately
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		// Try to serve the exact file
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		f, err := fs.Open(path)
		if err == nil {
			defer f.Close()
			stat, _ := f.Stat()
			if stat != nil && !stat.IsDir() {
				http.ServeContent(w, r, path, stat.ModTime(), f.(io.ReadSeeker))
				return
			}
		}

		// SPA fallback: serve index.html for any non-file path
		f, err = fs.Open("index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		stat, _ := f.Stat()
		http.ServeContent(w, r, "index.html", stat.ModTime(), f.(io.ReadSeeker))
	})

	// /api/chat-events — SSE streaming para eventos de chat no navegador
	mux.HandleFunc("/api/chat-events", func(w http.ResponseWriter, r *http.Request) {
		a.serveChatEventsSSE(w, r)
	})

	// /api/* — REST bridge mirroring Wails bindings
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		a.serveDebugAPI(w, r)
	})

	srv := debughttp.NewServer(
		&http.Server{
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		listener,
		port,
		bindAll,
	)

	a.debugHTTP = srv

	go func() {
		bindLabel := "127.0.0.1"
		if bindAll {
			bindLabel = "0.0.0.0 (rede)"
		}
		log.Printf("[debug-http] servidor iniciado em http://%s:%d", bindLabel, port)
		if err := srv.HTTP.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[debug-http] erro no servidor: %v", err)
		}
	}()

	return nil
}

// StartDebugHTTPServer binds a local-only HTTP server on a random port.
// The server serves static frontend assets and a /api/* REST bridge.
func (a *App) StartDebugHTTPServer() error {
	return a.startDebugHTTPInternal("127.0.0.1", 0)
}

// StopDebugHTTPServer gracefully shuts down the debug HTTP server.
func (a *App) StopDebugHTTPServer() {
	if a.debugHTTP == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.debugHTTP.HTTP.Shutdown(ctx); err != nil {
		log.Printf("[debug-http] erro ao parar servidor: %v", err)
	}
	a.debugHTTP.Listener.Close()
	a.debugHTTP = nil
	log.Println("[debug-http] servidor parado")
}

// GetDebugHTTPPort returns the port the debug HTTP server is listening on, or
// falls back to the dedicated chat SSE server port. Returns 0 if neither is running.
func (a *App) GetDebugHTTPPort() int {
	if a.debugHTTP != nil {
		return a.debugHTTP.Port
	}
	return a.GetChatSSEPort()
}

// GetChatSSEPort returns the port of the dedicated chat SSE server (always on
// loopback), or 0 if it could not be started.
func (a *App) GetChatSSEPort() int {
	if a.chatSSE == nil {
		return 0
	}
	return a.chatSSE.Port
}

// PollChatEvents retorna todos os eventos de chat pendentes no buffer de
// polling e os remove. Usado pelo frontend nativo (WebView2) como alternativa
// ao EventSource quando o SSE é bloqueado por mixed-content.
// Retorna um array JSON de strings (cada string é um evento JSON no formato
// {"event":"chat:token","data":"..."}), ou array vazio se não houver eventos.
//
// O frontend deve chamar este método em polling (ex.: a cada 100ms) enquanto
// houver um stream ativo (chatSending=true). O polling é interrompido quando
// recebe chat:done/chat:error/chat:stopped.
func (a *App) PollChatEvents() string {
	if a.chatEvents == nil {
		return "[]"
	}
	events := a.chatEvents.DrainPollBuffer()
	if len(events) == 0 {
		return "[]"
	}
	// Usa encoding/json para serialização segura (evita concatenação manual de
	// JSON que quebraria com strings contendo aspas/barras).
	b, err := json.Marshal(events)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// EnsureChatSSEServer starts a minimal SSE-only HTTP server on 127.0.0.1 that
// serves only /api/chat-events. This is always active (even outside debug mode)
// so the native webview can reliably receive chat streaming events via SSE when
// Wails v3 native event delivery is unreliable.
func (a *App) EnsureChatSSEServer() error {
	if a.chatSSE != nil {
		return nil // already running
	}
	if a.chatEvents == nil {
		a.chatEvents = debughttp.NewChatEventBroker()
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("chat-sse: falha ao criar listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat-events", func(w http.ResponseWriter, r *http.Request) {
		a.serveChatEventsSSE(w, r)
	})

	srv := debughttp.NewServer(
		&http.Server{
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		listener,
		port,
		false, // sempre loopback-only
	)
	a.chatSSE = srv

	go func() {
		log.Printf("[chat-sse] servidor SSE dedicado iniciado em http://127.0.0.1:%d", port)
		if err := srv.HTTP.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[chat-sse] erro no servidor: %v", err)
		}
	}()

	return nil
}

// StopChatSSEServer gracefully shuts down the dedicated chat SSE server.
func (a *App) StopChatSSEServer() {
	if a.chatSSE == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.chatSSE.HTTP.Shutdown(ctx); err != nil {
		log.Printf("[chat-sse] erro ao parar servidor: %v", err)
	}
	a.chatSSE.Listener.Close()
	a.chatSSE = nil
	log.Println("[chat-sse] servidor parado")
}

// IsDebugHTTPBoundToAllInterfaces returns whether the debug HTTP server is bound
// to 0.0.0.0 (all network interfaces) instead of the default 127.0.0.1.
func (a *App) IsDebugHTTPBoundToAllInterfaces() bool {
	if a.debugHTTP == nil {
		return false
	}
	return a.debugHTTP.BindAllInterfaces
}

// SetDebugHTTPBindAllInterfaces restarts the debug HTTP server to bind on
// 0.0.0.0 (when enabled=true) or 127.0.0.1 (when enabled=false).
// Preserves the current port so tray menu "Abrir no navegador" and other
// references remain valid after the rebind.
func (a *App) SetDebugHTTPBindAllInterfaces(enabled bool) error {
	if a.debugHTTP == nil {
		return fmt.Errorf("servidor debug-http nao esta em execucao")
	}
	if a.debugHTTP.BindAllInterfaces == enabled {
		// Already in the requested state — no-op
		return nil
	}

	// Preserve the current port so references (tray, logs) stay valid.
	currentPort := a.debugHTTP.Port

	// Stop the current listener/server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.debugHTTP.HTTP.Shutdown(ctx); err != nil {
		log.Printf("[debug-http] aviso ao parar servidor para rebind: %v", err)
	}
	a.debugHTTP.Listener.Close()
	a.debugHTTP = nil

	// Restart with the new bind address, preserving the port.
	bindAddr := "127.0.0.1"
	if enabled {
		bindAddr = "0.0.0.0"
	}

	if err := a.startDebugHTTPInternal(bindAddr, currentPort); err != nil {
		// If the port was somehow taken, fall back to random port
		log.Printf("[debug-http] falha ao reiniciar na porta %d, tentando porta aleatoria: %v", currentPort, err)
		if err2 := a.startDebugHTTPInternal(bindAddr, 0); err2 != nil {
			return fmt.Errorf("falha ao reiniciar debug-http com bind %s: %w", bindAddr, err2)
		}
	}

	if enabled {
		log.Printf("[debug-http] servidor reiniciado em 0.0.0.0:%d (acessivel na rede)", a.debugHTTP.Port)
	} else {
		log.Printf("[debug-http] servidor reiniciado em 127.0.0.1:%d (somente local)", a.debugHTTP.Port)
	}
	return nil
}

// resolveDebugCORSOrigin returns the CORS header value for the debug HTTP server.
func (a *App) resolveDebugCORSOrigin() string {
	if a.debugHTTP != nil && a.debugHTTP.BindAllInterfaces {
		return "*"
	}
	if a.debugHTTP != nil {
		return "http://127.0.0.1:" + fmt.Sprint(a.debugHTTP.Port)
	}
	return "http://localhost"
}

// setSSECORSOrigin define o header Access-Control-Allow-Origin do endpoint SSE
// de chat. O navegador (modo debug HTTP) já era atendido por resolveDebugCORSOrigin.
// Para permitir que o webview nativo Wails consuma o mesmo broker SSE, ecoamos o
// Origin da requisição quando ele for seguro (loopback, host vazio/paginado ou
// igual ao origin do debug HTTP). Nunca ecoamos um Origin arbitrário de rede.
func (a *App) setSSECORSOrigin(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Sem Origin (curl, EventSource local de mesma origem): reflete o padrão.
		w.Header().Set("Access-Control-Allow-Origin", a.resolveDebugCORSOrigin())
		return
	}

	// Loopback, host "localhost" (inclui o origin do webview nativo Wails v3,
	// que usa "http://wails.localhost" — que resolve para 127.0.0.1) e
	// null-origin são sempre seguros para ecoar (fetch/EventSource de arquivos
	// locais ou do webview nativo podem usar "null"/"wails.localhost").
	lower := strings.ToLower(origin)
	if strings.HasPrefix(lower, "http://127.0.0.1:") ||
		strings.HasPrefix(lower, "http://localhost:") ||
		strings.HasPrefix(lower, "http://[::1]:") ||
		strings.HasSuffix(origin, "://wails.localhost") ||
		lower == "wails://app" ||
		lower == "null" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		return
	}

	// Se a origem for idêntica à autoridade do debug HTTP (ex.: outro origin
	// loopback da mesma máquina), ecoa. Caso contrário, recua para o padrão.
	w.Header().Set("Access-Control-Allow-Origin", a.resolveDebugCORSOrigin())
}

// serveDebugAPI handles /api/<method> requests, dispatching to the corresponding App method.
func (a *App) serveDebugAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", a.resolveDebugCORSOrigin())
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	methodName := strings.TrimPrefix(r.URL.Path, "/api/")
	methodName = strings.TrimSuffix(methodName, "/")
	if methodName == "" {
		a.serveDebugAPIList(w, r)
		return
	}

	// Use reflection to call the method on App
	v := reflect.ValueOf(a)
	m := v.MethodByName(methodName)
	if !m.IsValid() {
		a.writeAPIError(w, http.StatusNotFound, "método não encontrado: "+methodName)
		return
	}

	mt := m.Type()

	var args []reflect.Value

	switch mt.NumIn() {
	case 0:
		// No arguments — just call it
	case 1:
		// One argument — read from request body
		argType := mt.In(0)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			a.writeAPIError(w, http.StatusBadRequest, "falha ao ler corpo da requisicao: "+err.Error())
			return
		}

		arg, err := a.unmarshalAPIParam(string(body), argType)
		if err != nil {
			a.writeAPIError(w, http.StatusBadRequest, "parametro invalido para "+methodName+": "+err.Error())
			return
		}
		args = append(args, reflect.ValueOf(arg))
	default:
		a.writeAPIError(w, http.StatusBadRequest, "metodo com multiplos parametros nao suportado via API HTTP")
		return
	}

	results := m.Call(args)

	switch len(results) {
	case 0:
		w.WriteHeader(http.StatusNoContent)
	case 1:
		a.writeAPIJSON(w, http.StatusOK, results[0].Interface())
	case 2:
		// Most common: (result, error)
		resultVal := results[0]
		errVal := results[1]

		if !errVal.IsNil() {
			errMsg := errVal.Interface().(error).Error()
			a.writeAPIError(w, http.StatusInternalServerError, errMsg)
			return
		}
		a.writeAPIJSON(w, http.StatusOK, resultVal.Interface())
	default:
		a.writeAPIError(w, http.StatusInternalServerError, "numero inesperado de retornos")
	}
}

// unmarshalAPIParam converts a raw JSON string body into the expected argument type.
func (a *App) unmarshalAPIParam(raw string, targetType reflect.Type) (interface{}, error) {
	raw = strings.TrimSpace(raw)

	switch targetType.Kind() {
	case reflect.String:
		// Unwrap JSON string quotes if present
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			// Accept plain string without quotes
			return raw, nil
		}
		return s, nil
	default:
		// Create a new instance of the target type and unmarshal into it
		ptr := reflect.New(targetType)
		if err := json.Unmarshal([]byte(raw), ptr.Interface()); err != nil {
			return nil, fmt.Errorf("falha ao decodificar %s: %w", targetType.Name(), err)
		}
		return ptr.Elem().Interface(), nil
	}
}

func (a *App) serveDebugAPIList(w http.ResponseWriter, _ *http.Request) {
	v := reflect.ValueOf(a)
	t := v.Type()
	var methods []string
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		// Only include exported methods with 0 or 1 parameters (excluding context.Context)
		if m.PkgPath != "" {
			continue
		}
		mt := m.Type
		if mt.NumIn() > 2 { // receiver + arg (we check for context separately)
			continue
		}
		// Skip methods that take context.Context as first argument
		if mt.NumIn() == 2 && mt.In(1).String() == "context.Context" {
			continue
		}
		methods = append(methods, m.Name)
	}
	displayHost := "127.0.0.1"
	if a.debugHTTP != nil && a.debugHTTP.BindAllInterfaces {
		displayHost = "0.0.0.0"
	}
	a.writeAPIJSON(w, http.StatusOK, map[string]interface{}{
		"methods":           methods,
		"port":              a.debugHTTP.Port,
		"url":               fmt.Sprintf("http://%s:%d", displayHost, a.debugHTTP.Port),
		"bindAllInterfaces": a.debugHTTP != nil && a.debugHTTP.BindAllInterfaces,
	})
}

func (a *App) writeAPIJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[debug-http] erro ao serializar resposta: %v", err)
	}
}

func (a *App) writeAPIError(w http.ResponseWriter, status int, message string) {
	a.writeAPIJSON(w, status, map[string]string{"error": message})
}

// serveChatEventsSSE mantém uma conexão SSE (Server-Sent Events) para
// transmitir eventos de streaming de chat ao navegador no modo debug HTTP.
// O frontend usa esse endpoint como fallback para window.runtime.EventsOn.
func (a *App) serveChatEventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming nao suportado", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Um webview nativo Wails pode obter eventos de chat reutilizando o mesmo
	// broker SSE do navegador. Como o Origin do webview é um scheme custom
	// (ex.: http://wails.localhost), ecoamos o Origin da requisição quando a
	// origem é confiável (loopback/identical), em vez de só aceitar o origin
	// fixo do debug HTTP. EventSource cross-origin sem esta header é bloqueado
	// pelo navegador e o chat nativo mostraria "Tempo limite" (com o backend
	// completando normalmente — ver chat-native-event-loss.md).
	a.setSSECORSOrigin(w, r)

	if a.chatEvents == nil {
		http.Error(w, "chat events indisponivel", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	ch := a.chatEvents.Subscribe()
	defer a.chatEvents.Unsubscribe(ch)

	// Envia um evento inicial para confirmar conexão
	fmt.Fprintf(w, "data: {\"event\":\"chat:connected\",\"data\":\"\"}\n\n")
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}
