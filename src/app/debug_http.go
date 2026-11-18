//go:build windows

package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"discovery/app/core/runtimediag"
	"discovery/app/debughttp"
)

// ChatSSEInfo expõe porta, URL e token do servidor SSE local para o webview nativo.
type ChatSSEInfo = debughttp.ChatSSEInfo

// debugBlockedReflectionMethods lista métodos do *App que NUNCA podem ser
// invocados ou listados via reflection HTTP (/api/<method>).
var debugBlockedReflectionMethods = map[string]bool{
	"GetChatSSEInfo":  true,
	"GetDebugHTTPURL": true,
}

func (a *App) ensureDebugAuthToken() string {
	a.debugAuthTokenMu.Lock()
	defer a.debugAuthTokenMu.Unlock()
	if a.debugAuthToken != "" {
		return a.debugAuthToken
	}
	tok, err := debughttp.GenerateAuthToken()
	if err != nil {
		// Fallback determinístico-impossível não permitido; usa timestamp+pid só se CSPRNG falhar.
		tok = fmt.Sprintf("%x", time.Now().UnixNano())
	}
	a.debugAuthToken = tok
	return tok
}

// PublishChatEvent publica um evento de chat para os inscritos SSE.
// Chamado por StartChatStream para forward dos eventos Wails -> HTTP/IPC.
func (a *App) PublishChatEvent(eventType, data string) {
	if a.chatEvents != nil {
		a.chatEvents.Publish(eventType, data)
	}
	if a.RuntimeFlags.ServiceMode && a.ipcServer != nil && a.ipcServer.ClientCount() > 0 {
		a.broadcastIPCEvent("chat:sse_event", map[string]any{
			"eventType": eventType,
			"data":      data,
		})
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
	authToken := a.ensureDebugAuthToken()

	var srv *debughttp.Server
	mux := http.NewServeMux()

	// Static file serving from embedded FS (SPA fallback to index.html)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !a.validateDebugHost(w, r, srv) {
			return
		}
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
		if !a.validateDebugRequest(w, r, srv, true) {
			return
		}
		a.serveChatEventsSSE(w, r, srv)
	})

	// /api/internal/runtime-memory — fotografia de memória do runtime Go
	mux.HandleFunc("/api/internal/runtime-memory", func(w http.ResponseWriter, r *http.Request) {
		if !a.validateDebugRequest(w, r, srv, false) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(runtimediag.Take()); err != nil {
			log.Printf("[debug-http] falha ao serializar memória do runtime: %v", err)
		}
	})

	// /api/* — REST bridge mirroring Wails bindings
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		a.serveDebugAPI(w, r, srv)
	})

	srv = debughttp.NewServer(
		&http.Server{
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		listener,
		port,
		bindAll,
		authToken,
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
	_ = a.debugHTTP.Listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.debugHTTP.HTTP.Shutdown(ctx); err != nil {
		log.Printf("[debug-http] erro ao parar servidor: %v", err)
	}
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

// GetDebugHTTPURL retorna a URL completa de abertura no navegador com #token=
// (fragmento nunca enviado em headers HTTP/Referer). Bloqueada de /api/.
func (a *App) GetDebugHTTPURL() string {
	port := a.GetDebugHTTPPort()
	if port <= 0 {
		return ""
	}
	tok := a.ensureDebugAuthToken()
	return fmt.Sprintf("http://127.0.0.1:%d/#token=%s", port, url.QueryEscape(tok))
}

// GetChatSSEPort returns the port of the dedicated chat SSE server (always on
// loopback), or 0 if it could not be started.
func (a *App) GetChatSSEPort() int {
	if a.chatSSE == nil {
		return 0
	}
	return a.chatSSE.Port
}

// GetChatSSEInfo devolve porta, URL e token do servidor SSE local para o webview
// nativo (via binding Wails; bloqueado do servidor HTTP reflection).
func (a *App) GetChatSSEInfo() ChatSSEInfo {
	port := a.GetChatSSEPort()
	if port <= 0 {
		return ChatSSEInfo{}
	}
	tok := ""
	if a.chatSSE != nil {
		tok = a.chatSSE.AuthToken
	}
	if tok == "" {
		tok = a.ensureDebugAuthToken()
	}
	return ChatSSEInfo{
		Port:  port,
		URL:   fmt.Sprintf("http://127.0.0.1:%d/api/chat-events", port),
		Token: tok,
	}
}

// PollChatEvents retorna todos os eventos de chat pendentes no buffer de
// polling e os remove. Usado pelo frontend nativo (WebView2) como alternativa
// ao EventSource quando o SSE é bloqueado por mixed-content.
func (a *App) PollChatEvents() string {
	if a.chatEvents == nil {
		return "[]"
	}
	events := a.chatEvents.DrainPollBuffer()
	if subDropped, pollOverwritten := a.chatEvents.DrainDrops(); subDropped > 0 || pollOverwritten > 0 {
		log.Printf("[chat-sse] broker: descarte de eventos — inscrito SSE lento=%d, buffer de polling sobrescrito=%d", subDropped, pollOverwritten)
	}
	if len(events) == 0 {
		return "[]"
	}
	b, err := json.Marshal(events)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// EnsureChatSSEServer starts a minimal SSE-only HTTP server on 127.0.0.1 that
// serves only /api/chat-events (autenticado por token + Host loopback).
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
	authToken := a.ensureDebugAuthToken()

	var srv *debughttp.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/chat-events", func(w http.ResponseWriter, r *http.Request) {
		if !a.validateDebugRequest(w, r, srv, true) {
			return
		}
		a.serveChatEventsSSE(w, r, srv)
	})

	srv = debughttp.NewServer(
		&http.Server{
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		listener,
		port,
		false, // sempre loopback-only
		authToken,
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
	_ = a.chatSSE.Listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.chatSSE.HTTP.Shutdown(ctx); err != nil {
		log.Printf("[chat-sse] erro ao parar servidor: %v", err)
	}
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
func (a *App) SetDebugHTTPBindAllInterfaces(enabled bool) error {
	if a.debugHTTP == nil {
		return fmt.Errorf("servidor debug-http nao esta em execucao")
	}
	if a.debugHTTP.BindAllInterfaces == enabled {
		return nil
	}

	currentPort := a.debugHTTP.Port

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.debugHTTP.HTTP.Shutdown(ctx); err != nil {
		log.Printf("[debug-http] aviso ao parar servidor para rebind: %v", err)
	}
	a.debugHTTP.Listener.Close()
	a.debugHTTP = nil

	bindAddr := "127.0.0.1"
	if enabled {
		bindAddr = "0.0.0.0"
	}

	if err := a.startDebugHTTPInternal(bindAddr, currentPort); err != nil {
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

func (a *App) validateDebugHost(w http.ResponseWriter, r *http.Request, srv *debughttp.Server) bool {
	if srv == nil || srv.Port <= 0 {
		a.writeAPIError(w, http.StatusForbidden, "host validation unavailable")
		return false
	}
	rawHost := strings.TrimSpace(r.Host)
	if rawHost == "" || strings.ContainsAny(rawHost, " \t\r\n/\\@") {
		a.writeAPIError(w, http.StatusForbidden, "host invalido")
		return false
	}
	hostname, portStr, err := net.SplitHostPort(rawHost)
	if err != nil {
		if srv.Port != 80 {
			a.writeAPIError(w, http.StatusForbidden, "host sem porta esperada")
			return false
		}
		hostname = strings.Trim(rawHost, "[]")
		portStr = "80"
	}
	if portStr != strconv.Itoa(srv.Port) {
		a.writeAPIError(w, http.StatusForbidden, "porta de host invalida")
		return false
	}
	if !srv.BindAllInterfaces {
		if !strings.EqualFold(hostname, "localhost") && hostname != "127.0.0.1" && hostname != "::1" {
			a.writeAPIError(w, http.StatusForbidden, "host fora de loopback rejeitado")
			return false
		}
	}
	return true
}

func extractDebugTokenFromRequest(r *http.Request, allowQueryToken bool) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
		if tok := strings.TrimSpace(auth[7:]); tok != "" {
			return tok
		}
	}
	if tok := strings.TrimSpace(r.Header.Get("X-Discovery-Debug-Token")); tok != "" {
		return tok
	}
	if allowQueryToken {
		if tok := strings.TrimSpace(r.URL.Query().Get("token")); tok != "" {
			return tok
		}
	}
	return ""
}

func (a *App) validateDebugToken(w http.ResponseWriter, r *http.Request, srv *debughttp.Server, allowQueryToken bool) bool {
	expected := ""
	if srv != nil {
		expected = srv.AuthToken
	}
	got := extractDebugTokenFromRequest(r, allowQueryToken)
	if expected == "" || len(got) != len(expected) || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		a.writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

func (a *App) validateDebugRequest(w http.ResponseWriter, r *http.Request, srv *debughttp.Server, allowQueryToken bool) bool {
	if !a.validateDebugHost(w, r, srv) {
		return false
	}
	return a.validateDebugToken(w, r, srv, allowQueryToken)
}

func (a *App) isAllowedDebugPort(portStr string, srv *debughttp.Server) bool {
	if portStr == "" {
		return false
	}
	if srv != nil && portStr == strconv.Itoa(srv.Port) {
		return true
	}
	if a.debugHTTP != nil && portStr == strconv.Itoa(a.debugHTTP.Port) {
		return true
	}
	if a.chatSSE != nil && portStr == strconv.Itoa(a.chatSSE.Port) {
		return true
	}
	return false
}

// resolveDebugCORSOrigin valida estritamente o Origin contra origens loopback
// da própria porta (ou a autoridade exata do Host quando BindAllInterfaces=true).
// Nunca devolve "*" nem ecoa "null".
func (a *App) resolveDebugCORSOrigin(r *http.Request, srv *debughttp.Server) string {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || strings.EqualFold(origin, "null") {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" {
		return ""
	}
	uHost := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	uPort := strings.TrimSpace(parsed.Port())
	if (uHost == "127.0.0.1" || uHost == "localhost" || uHost == "::1") && a.isAllowedDebugPort(uPort, srv) {
		return origin
	}
	if srv != nil && srv.BindAllInterfaces && strings.EqualFold(parsed.Host, strings.TrimSpace(r.Host)) {
		return origin
	}
	return ""
}

// setSSECORSOrigin define Access-Control-Allow-Origin exclusivamente para
// origens loopback confiáveis na porta do servidor ou origens nativas do Wails.
// Nunca ecoa "null" nem devolve "*".
func (a *App) setSSECORSOrigin(w http.ResponseWriter, r *http.Request, srv *debughttp.Server) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || strings.EqualFold(origin, "null") {
		return
	}
	if allowed := a.resolveDebugCORSOrigin(r, srv); allowed != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowed)
		w.Header().Set("Vary", "Origin")
		return
	}
	lower := strings.ToLower(origin)
	if lower == "http://wails.localhost" || lower == "https://wails.localhost" || lower == "wails://app" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
}

// serveDebugAPI handles /api/<method> requests, dispatching to the corresponding App method.
func (a *App) serveDebugAPI(w http.ResponseWriter, r *http.Request, srv *debughttp.Server) {
	if !a.validateDebugHost(w, r, srv) {
		return
	}
	if allowedOrigin := a.resolveDebugCORSOrigin(r, srv); allowedOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Discovery-Debug-Token")

	if r.Method == http.MethodOptions {
		if r.Header.Get("Origin") != "" && w.Header().Get("Access-Control-Allow-Origin") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if !a.validateDebugToken(w, r, srv, false) {
		return
	}

	methodName := strings.TrimPrefix(r.URL.Path, "/api/")
	methodName = strings.TrimSuffix(methodName, "/")
	if methodName == "" {
		a.serveDebugAPIList(w, r, srv)
		return
	}

	if debugBlockedReflectionMethods[methodName] {
		a.writeAPIError(w, http.StatusForbidden, "método bloqueado via HTTP: "+methodName)
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
		body, err := io.ReadAll(r.Body)
		if err != nil {
			a.writeAPIError(w, http.StatusBadRequest, "falha ao ler corpo da requisicao: "+err.Error())
			return
		}
		var rawArgs []json.RawMessage
		if err := json.Unmarshal(body, &rawArgs); err != nil {
			a.writeAPIError(w, http.StatusBadRequest, "body de multi-argumentos deve ser um array JSON: "+err.Error())
			return
		}
		if len(rawArgs) != mt.NumIn() {
			a.writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("numero de argumentos invalido para %s: recebido %d, esperado %d", methodName, len(rawArgs), mt.NumIn()))
			return
		}
		for i, raw := range rawArgs {
			arg, err := a.unmarshalAPIParam(string(raw), mt.In(i))
			if err != nil {
				a.writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("parametro invalido para %s (indice %d): %s", methodName, i, err.Error()))
				return
			}
			args = append(args, reflect.ValueOf(arg))
		}
	}

	results := m.Call(args)

	switch len(results) {
	case 0:
		w.WriteHeader(http.StatusNoContent)
	case 1:
		a.writeAPIJSON(w, http.StatusOK, results[0].Interface())
	case 2:
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
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return raw, nil
		}
		return s, nil
	default:
		ptr := reflect.New(targetType)
		if err := json.Unmarshal([]byte(raw), ptr.Interface()); err != nil {
			return nil, fmt.Errorf("falha ao decodificar %s: %w", targetType.Name(), err)
		}
		return ptr.Elem().Interface(), nil
	}
}

func (a *App) serveDebugAPIList(w http.ResponseWriter, _ *http.Request, srv *debughttp.Server) {
	v := reflect.ValueOf(a)
	t := v.Type()
	var methods []string
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		if m.PkgPath != "" || debugBlockedReflectionMethods[m.Name] {
			continue
		}
		mt := m.Type
		if mt.NumIn() > 2 {
			continue
		}
		if mt.NumIn() == 2 && mt.In(1).String() == "context.Context" {
			continue
		}
		methods = append(methods, m.Name)
	}
	displayHost := "127.0.0.1"
	port := 0
	bindAll := false
	if srv != nil {
		port = srv.Port
		bindAll = srv.BindAllInterfaces
	} else if a.debugHTTP != nil {
		port = a.debugHTTP.Port
		bindAll = a.debugHTTP.BindAllInterfaces
	}
	if bindAll {
		displayHost = "0.0.0.0"
	}
	a.writeAPIJSON(w, http.StatusOK, map[string]interface{}{
		"methods":           methods,
		"port":              port,
		"url":               fmt.Sprintf("http://%s:%d", displayHost, port),
		"bindAllInterfaces": bindAll,
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
func (a *App) serveChatEventsSSE(w http.ResponseWriter, r *http.Request, srv *debughttp.Server) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming nao suportado", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	a.setSSECORSOrigin(w, r, srv)

	if a.chatEvents == nil {
		http.Error(w, "chat events indisponivel", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	ch := a.chatEvents.Subscribe()
	defer a.chatEvents.Unsubscribe(ch)

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
