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
	"sync"
	"time"
)

var (
	frontendFS   http.FileSystem
	frontendFSMu sync.RWMutex
)

// ── Chat Event Broker (SSE para debug HTTP) ─────────────────────────────

// chatEventBroker gerencia inscritos SSE para eventos de streaming do chat.
// Permite que o chat funcione no navegador (debug HTTP) onde o runtime Wails
// não está disponível para receber EventsEmit/EventsOn.
type chatEventBroker struct {
	mu          sync.RWMutex
	subscribers map[chan string]struct{}
}

func newChatEventBroker() *chatEventBroker {
	return &chatEventBroker{
		subscribers: make(map[chan string]struct{}),
	}
}

func (b *chatEventBroker) subscribe() chan string {
	ch := make(chan string, 128)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *chatEventBroker) unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
}

// publish envia um evento JSON para todos os inscritos SSE.
// Formato: {"event":"chat:token","data":"conteudo"}
func (b *chatEventBroker) publish(eventType, data string) {
	payload, err := json.Marshal(map[string]string{
		"event": eventType,
		"data":  data,
	})
	if err != nil {
		return
	}
	line := string(payload)

	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- line:
		default:
			// descarta se inscrito estiver lento (buffer cheio)
		}
	}
}

// PublishChatEvent publica um evento de chat para os inscritos SSE.
// Chamado por StartChatStream para forward dos eventos Wails → HTTP.
func (a *App) PublishChatEvent(eventType, data string) {
	if a.chatEvents != nil {
		a.chatEvents.publish(eventType, data)
	}
}

// SetDebugFrontendAssets stores the embedded frontend filesystem for the debug HTTP server.
// The embed.FS from `//go:embed all:frontend` has a `frontend/` prefix — we strip it
// via fs.Sub so that the HTTP server can serve paths as `index.html`, `app.js`, etc.
// Must be called before App.startup() runs (i.e. before wails.Run).
func SetDebugFrontendAssets(fs http.FileSystem) {
	frontendFSMu.Lock()
	frontendFS = fs
	frontendFSMu.Unlock()
}

func getFrontendFS() http.FileSystem {
	frontendFSMu.RLock()
	defer frontendFSMu.RUnlock()
	return frontendFS
}

// debugHTTPServer serves the embedded frontend and a REST API mirroring the Wails bridge.
type debugHTTPServer struct {
	server            *http.Server
	listener          net.Listener
	port              int
	bindAllInterfaces bool
	app               *App
}

// startDebugHTTPInternal binds and starts the HTTP server on the given bind address.
// If port is 0, a random port is allocated. Otherwise the specified port is used.
func (a *App) startDebugHTTPInternal(bindAddr string, port int) error {
	fs := getFrontendFS()
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

	srv := &debugHTTPServer{
		server: &http.Server{
			Handler:      mux,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 60 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		listener:          listener,
		port:              port,
		bindAllInterfaces: bindAll,
		app:               a,
	}

	a.debugHTTP = srv

	go func() {
		bindLabel := "127.0.0.1"
		if bindAll {
			bindLabel = "0.0.0.0 (rede)"
		}
		log.Printf("[debug-http] servidor iniciado em http://%s:%d", bindLabel, port)
		if err := srv.server.Serve(listener); err != nil && err != http.ErrServerClosed {
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
	if err := a.debugHTTP.server.Shutdown(ctx); err != nil {
		log.Printf("[debug-http] erro ao parar servidor: %v", err)
	}
	a.debugHTTP.listener.Close()
	a.debugHTTP = nil
	log.Println("[debug-http] servidor parado")
}

// GetDebugHTTPPort returns the port the debug HTTP server is listening on, or 0 if not running.
func (a *App) GetDebugHTTPPort() int {
	if a.debugHTTP == nil {
		return 0
	}
	return a.debugHTTP.port
}

// IsDebugHTTPBoundToAllInterfaces returns whether the debug HTTP server is bound
// to 0.0.0.0 (all network interfaces) instead of the default 127.0.0.1.
func (a *App) IsDebugHTTPBoundToAllInterfaces() bool {
	if a.debugHTTP == nil {
		return false
	}
	return a.debugHTTP.bindAllInterfaces
}

// SetDebugHTTPBindAllInterfaces restarts the debug HTTP server to bind on
// 0.0.0.0 (when enabled=true) or 127.0.0.1 (when enabled=false).
// Preserves the current port so tray menu "Abrir no navegador" and other
// references remain valid after the rebind.
func (a *App) SetDebugHTTPBindAllInterfaces(enabled bool) error {
	if a.debugHTTP == nil {
		return fmt.Errorf("servidor debug-http nao esta em execucao")
	}
	if a.debugHTTP.bindAllInterfaces == enabled {
		// Already in the requested state — no-op
		return nil
	}

	// Preserve the current port so references (tray, logs) stay valid.
	currentPort := a.debugHTTP.port

	// Stop the current listener/server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.debugHTTP.server.Shutdown(ctx); err != nil {
		log.Printf("[debug-http] aviso ao parar servidor para rebind: %v", err)
	}
	a.debugHTTP.listener.Close()
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
		log.Printf("[debug-http] servidor reiniciado em 0.0.0.0:%d (acessivel na rede)", a.debugHTTP.port)
	} else {
		log.Printf("[debug-http] servidor reiniciado em 127.0.0.1:%d (somente local)", a.debugHTTP.port)
	}
	return nil
}

// resolveDebugCORSOrigin returns the CORS header value for the debug HTTP server.
func (a *App) resolveDebugCORSOrigin() string {
	if a.debugHTTP != nil && a.debugHTTP.bindAllInterfaces {
		return "*"
	}
	if a.debugHTTP != nil {
		return "http://127.0.0.1:" + fmt.Sprint(a.debugHTTP.port)
	}
	return "http://localhost"
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
	if a.debugHTTP != nil && a.debugHTTP.bindAllInterfaces {
		displayHost = "0.0.0.0"
	}
	a.writeAPIJSON(w, http.StatusOK, map[string]interface{}{
		"methods":           methods,
		"port":              a.debugHTTP.port,
		"url":               fmt.Sprintf("http://%s:%d", displayHost, a.debugHTTP.port),
		"bindAllInterfaces": a.debugHTTP != nil && a.debugHTTP.bindAllInterfaces,
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
	w.Header().Set("Access-Control-Allow-Origin", a.resolveDebugCORSOrigin())

	if a.chatEvents == nil {
		http.Error(w, "chat events indisponivel", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	ch := a.chatEvents.subscribe()
	defer a.chatEvents.unsubscribe(ch)

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
