package debughttp

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
)

// ChatSSEInfo expõe porta, URL e token do servidor SSE local para o webview nativo.
type ChatSSEInfo struct {
	Port  int    `json:"port"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Server serves the embedded frontend and a REST API mirroring the Wails bridge.
type Server struct {
	HTTP              *http.Server
	Listener          net.Listener
	Port              int
	BindAllInterfaces bool
	AuthToken         string
}

// GenerateAuthToken gera um segredo aleatório de 32 bytes (64 hex chars) por instância.
func GenerateAuthToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// NewServer cria um Server de debug HTTP.
func NewServer(srv *http.Server, listener net.Listener, port int, bindAll bool, authToken string) *Server {
	return &Server{
		HTTP:              srv,
		Listener:          listener,
		Port:              port,
		BindAllInterfaces: bindAll,
		AuthToken:         authToken,
	}
}
