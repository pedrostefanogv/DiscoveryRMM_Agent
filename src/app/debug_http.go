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
	server   *http.Server
	listener net.Listener
	port     int
	app      *App
}

// StartDebugHTTPServer binds a local-only HTTP server on a random port.
// The server serves static frontend assets and a /api/* REST bridge.
func (a *App) StartDebugHTTPServer() error {
	fs := getFrontendFS()
	if fs == nil {
		return fmt.Errorf("frontend assets não configurados — chame SetDebugFrontendAssets antes")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("falha ao criar listener debug-http: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port

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
		listener: listener,
		port:     port,
		app:      a,
	}

	a.debugHTTP = srv

	go func() {
		log.Printf("[debug-http] servidor iniciado em http://127.0.0.1:%d", port)
		if err := srv.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[debug-http] erro no servidor: %v", err)
		}
	}()

	return nil
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

// serveDebugAPI handles /api/<method> requests, dispatching to the corresponding App method.
func (a *App) serveDebugAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1:"+fmt.Sprint(a.debugHTTP.port))
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
	a.writeAPIJSON(w, http.StatusOK, map[string]interface{}{
		"methods": methods,
		"port":    a.debugHTTP.port,
		"url":     fmt.Sprintf("http://127.0.0.1:%d", a.debugHTTP.port),
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
