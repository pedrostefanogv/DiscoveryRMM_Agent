package debughttp

import (
	"net"
	"net/http"
)

// Server serves the embedded frontend and a REST API mirroring the Wails bridge.
type Server struct {
	HTTP              *http.Server
	Listener          net.Listener
	Port              int
	BindAllInterfaces bool
}

// NewServer cria um Server de debug HTTP.
func NewServer(srv *http.Server, listener net.Listener, port int, bindAll bool) *Server {
	return &Server{
		HTTP:              srv,
		Listener:          listener,
		Port:              port,
		BindAllInterfaces: bindAll,
	}
}
