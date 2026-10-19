//go:build windows

package app

import (
	"bufio"
	"net"
	"testing"
	"time"
)

// REQUISITO: uma UI TRAVADA precisa parar de contar como "interface conectada".
//
// O rxWatchdog que existe hoje roda DENTRO da UI (IPCClient) — se o processo
// trava, ele não roda e o pipe continua aberto: ClientCount() ficava > 0 e o
// serviço continuava respondendo a convite de chat como se houvesse interface,
// enquanto nada aparecia na tela do usuário.
func TestIPCServer_SweepIdleConns(t *testing.T) {
	server := &IPCServer{conns: make(map[net.Conn]*ipcClientConn)}

	freshConn, freshPeer := net.Pipe()
	defer freshConn.Close()
	defer freshPeer.Close()

	staleConn, stalePeer := net.Pipe()
	defer staleConn.Close()
	defer stalePeer.Close()

	fresh := &ipcClientConn{reader: bufio.NewReader(freshConn)}
	fresh.touch()

	stale := &ipcClientConn{reader: bufio.NewReader(staleConn)}
	stale.touch()
	// Última mensagem bem antes do teto de inatividade.
	stale.lastActivity.Store(time.Now().Add(-(ipcServerIdleTimeout + 5*time.Second)).UnixMilli())

	server.conns[freshConn] = fresh
	server.conns[staleConn] = stale

	if dropped := server.sweepIdleConns(time.Now()); dropped != 1 {
		t.Fatalf("esperado derrubar 1 conexão travada; derrubou %d", dropped)
	}
	if got := server.ClientCount(); got != 1 {
		t.Fatalf("só a UI viva deve continuar contando; ClientCount=%d", got)
	}
	if _, ok := server.conns[freshConn]; !ok {
		t.Fatal("a UI que segue mandando mensagens não pode ser desconectada")
	}
	if _, ok := server.conns[staleConn]; ok {
		t.Fatal("a UI travada precisa sair do mapa de conexões")
	}
}

// Request em execução (ex.: instalação da loja) não pode ser derrubado pelo
// sweep enquanto o serviço trabalha: a conexão está viva, só não manda nada.
func TestIPCServer_SweepIdleConns_KeepsInFlightRequest(t *testing.T) {
	server := &IPCServer{conns: make(map[net.Conn]*ipcClientConn)}

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	client := &ipcClientConn{reader: bufio.NewReader(conn)}
	client.touch()
	// Ociosa há muito tempo (seria derrubada), mas com request em voo.
	client.lastActivity.Store(time.Now().Add(-(ipcServerIdleTimeout + 5*time.Second)).UnixMilli())
	client.inFlight.Store(1)
	server.conns[conn] = client

	if dropped := server.sweepIdleConns(time.Now()); dropped != 0 {
		t.Fatalf("conexão com request em voo não pode ser derrubada; derrubou %d", dropped)
	}
	if _, ok := server.conns[conn]; !ok {
		t.Fatal("conexão com request em voo saiu do mapa de conexões")
	}

	// Terminado o request, a ociosidade volta a valer.
	client.inFlight.Store(0)
	if dropped := server.sweepIdleConns(time.Now()); dropped != 1 {
		t.Fatalf("após o request, a conexão ociosa deve ser derrubada; derrubou %d", dropped)
	}
}

// Conexão recém-criada (sem nenhuma mensagem ainda) não é derrubada: a UI pode
// estar no handshake.
func TestIPCServer_SweepIdleConns_KeepsFreshConnWithoutActivity(t *testing.T) {
	server := &IPCServer{conns: make(map[net.Conn]*ipcClientConn)}

	conn, peer := net.Pipe()
	defer conn.Close()
	defer peer.Close()

	server.conns[conn] = &ipcClientConn{reader: bufio.NewReader(conn)}

	if dropped := server.sweepIdleConns(time.Now()); dropped != 0 {
		t.Fatalf("conexão nova não pode ser derrubada; derrubou %d", dropped)
	}
	if got := server.ClientCount(); got != 1 {
		t.Fatalf("ClientCount=%d, esperado 1", got)
	}
}

// O teto do sweep precisa ser folgado em relação à cadência da UI (snapshot de
// status a cada 5s), senão uma UI saudável seria derrubada e reconectaria em loop.
func TestIPCServer_IdleTimeoutIsSafeForStatusCadence(t *testing.T) {
	if ipcServerIdleTimeout < 10*DefaultCompanionConfig().PollInterval {
		t.Fatalf("teto de inatividade (%s) curto demais para a cadência de %s",
			ipcServerIdleTimeout, DefaultCompanionConfig().PollInterval)
	}
}
