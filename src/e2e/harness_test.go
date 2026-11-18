package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"golang.org/x/sys/windows"

	"discovery/app/core/remotesession"
)

// srcRoot retorna o caminho absoluto de Discovery/src a partir do cwd do teste.
func srcRoot(t testing.TB) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod nao encontrado subindo de %s", wd)
		}
		dir = parent
	}
}

// repoRoot retorna a raiz do repositorio Discovery (pai de Discovery/src).
func repoRoot(t testing.TB) string {
	t.Helper()
	return filepath.Dir(srcRoot(t))
}

func readRepoFile(t testing.TB, relPath string) string {
	t.Helper()
	full := filepath.Join(repoRoot(t), relPath)
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", full, err)
	}
	return string(b)
}

func readSrcFile(t testing.TB, relPath string) string {
	t.Helper()
	full := filepath.Join(srcRoot(t), relPath)
	b, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", full, err)
	}
	return string(b)
}

// ── Embedded NATS Server Harness ────────────────────────────────────────────

type tenantAuthFunc func(c server.ClientAuthentication) bool

func (f tenantAuthFunc) Check(c server.ClientAuthentication) bool {
	return f(c)
}

func startEmbeddedNATS(t testing.TB, enableJetStream bool, auth server.Authentication) (*server.Server, string) {
	t.Helper()
	opts := &server.Options{
		Host:                       "127.0.0.1",
		Port:                       -1,
		NoLog:                      true,
		NoSigs:                     true,
		MaxPayload:                 8 * 1024 * 1024,
		JetStream:                  enableJetStream,
		StoreDir:                   t.TempDir(),
		CustomClientAuthentication: auth,
	}
	s, err := server.NewServer(opts)
	if err != nil {
		t.Fatalf("server.NewServer: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		s.Shutdown()
		t.Fatalf("nats-server nao ficou pronto em 10s")
	}
	t.Cleanup(func() {
		s.Shutdown()
		s.WaitForShutdown()
	})
	return s, s.ClientURL()
}

func newTestNATSClient(t testing.TB, url string, token string) *nats.Conn {
	t.Helper()
	var opts []nats.Option
	opts = append(opts, nats.Name("e2e-client"), nats.Timeout(5*time.Second))
	if token != "" {
		opts = append(opts, nats.Token(token))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		t.Fatalf("nats.Connect(%s): %v", url, err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func newTestSessionManager(t testing.TB, nc *nats.Conn, clientID, siteID, agentID string) *remotesession.Manager {
	t.Helper()
	mgr := remotesession.NewManager(nil)
	mgr.SetNatsConn(nc, clientID, siteID, agentID)
	t.Cleanup(func() {
		mgr.StopAll()
		_ = mgr.Shutdown()
	})
	return mgr
}

func stripHyphens(id string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(id)), "-", "")
}

func sessionSubject(clientID, siteID, agentID, sessionID, suffix string) string {
	return fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.%s.%s",
		stripHyphens(clientID),
		stripHyphens(siteID),
		stripHyphens(agentID),
		stripHyphens(sessionID),
		suffix,
	)
}

func subscribeSync(t testing.TB, nc *nats.Conn, subject string) *nats.Subscription {
	t.Helper()
	sub, err := nc.SubscribeSync(subject)
	if err != nil {
		t.Fatalf("SubscribeSync(%s): %v", subject, err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("Flush after SubscribeSync(%s): %v", subject, err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return sub
}

func waitSubMsg(t testing.TB, sub *nats.Subscription, timeout time.Duration) *nats.Msg {
	t.Helper()
	msg, err := sub.NextMsg(timeout)
	if err != nil {
		t.Fatalf("NextMsg(%s, %v): %v", sub.Subject, timeout, err)
	}
	return msg
}

func assertNoSubMsg(t testing.TB, sub *nats.Subscription, wait time.Duration) {
	t.Helper()
	msg, err := sub.NextMsg(wait)
	if err == nil {
		t.Fatalf("esperava 0 mensagens em %s, mas recebeu %d bytes: %s", sub.Subject, len(msg.Data), string(msg.Data))
	}
	if !errors.Is(err, nats.ErrTimeout) {
		t.Fatalf("erro inesperado em NextMsg(%s): %v", sub.Subject, err)
	}
}

// ── Loopback HTTP & TCP Targets ─────────────────────────────────────────────

func startLoopbackHTTPServer(t testing.TB, handler http.Handler) (*httptest.Server, string, int) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%s): %v", srv.Listener.Addr().String(), err)
	}
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	return srv, srv.URL, port
}

func startLoopbackTCPEcho(t testing.TB) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	conns := make(map[net.Conn]struct{})
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-done:
					return
				default:
					return
				}
			}
			mu.Lock()
			conns[conn] = struct{}{}
			mu.Unlock()
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer func() {
					mu.Lock()
					delete(conns, c)
					mu.Unlock()
					_ = c.Close()
				}()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	stop := func() {
		select {
		case <-done:
		default:
			close(done)
			_ = ln.Close()
			mu.Lock()
			for c := range conns {
				_ = c.Close()
			}
			mu.Unlock()
		}
	}
	t.Cleanup(func() {
		stop()
		wg.Wait()
	})
	return ln.Addr().(*net.TCPAddr).Port, stop
}

func startLoopbackTCPBannerServer(t testing.TB, prefix []byte) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	conns := make(map[net.Conn]struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns[conn] = struct{}{}
			mu.Unlock()
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer func() {
					mu.Lock()
					delete(conns, c)
					mu.Unlock()
					_ = c.Close()
				}()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						out := append(append([]byte{}, prefix...), buf[:n]...)
						if _, werr := c.Write(out); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().(*net.TCPAddr).Port
}

func freeTCPPort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// ── Binary 'DF' File Frame Codec (Contract 5) ───────────────────────────────

func encodeBinaryFileFrame(header map[string]any, payload []byte) []byte {
	hdrBytes, _ := json.Marshal(header)
	out := make([]byte, 6+len(hdrBytes)+len(payload))
	out[0] = 'D'
	out[1] = 'F'
	binary.BigEndian.PutUint32(out[2:6], uint32(len(hdrBytes)))
	copy(out[6:6+len(hdrBytes)], hdrBytes)
	copy(out[6+len(hdrBytes):], payload)
	return out
}

func decodeBinaryFileFrame(raw []byte) (map[string]any, []byte, error) {
	if len(raw) < 6 {
		return nil, nil, fmt.Errorf("frame DF truncado: %d bytes", len(raw))
	}
	if raw[0] != 'D' || raw[1] != 'F' {
		return nil, nil, fmt.Errorf("magic invalido: %x %x", raw[0], raw[1])
	}
	hdrLen := int(binary.BigEndian.Uint32(raw[2:6]))
	if hdrLen <= 0 || 6+hdrLen > len(raw) {
		return nil, nil, fmt.Errorf("headerLen invalido: %d (frame=%d)", hdrLen, len(raw))
	}
	var hdr map[string]any
	if err := json.Unmarshal(raw[6:6+hdrLen], &hdr); err != nil {
		return nil, nil, fmt.Errorf("header json invalido: %w", err)
	}
	return hdr, raw[6+hdrLen:], nil
}

// ── Windows DPAPI & icacls Helpers ──────────────────────────────────────────

const (
	cryptProtectUIForbidden  = 0x01
	cryptProtectLocalMachine = 0x04
)

func dpapiProtect(plaintext []byte) ([]byte, error) {
	var inBlob windows.DataBlob
	if len(plaintext) > 0 {
		inBlob.Data = &plaintext[0]
		inBlob.Size = uint32(len(plaintext))
	}
	var outBlob windows.DataBlob
	err := windows.CryptProtectData(&inBlob, nil, nil, 0, nil, cryptProtectLocalMachine|cryptProtectUIForbidden, &outBlob)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
	out := make([]byte, outBlob.Size)
	if outBlob.Size > 0 {
		copy(out, unsafe.Slice(outBlob.Data, outBlob.Size))
	}
	return out, nil
}

func dpapiUnprotect(ciphertext []byte) ([]byte, error) {
	var inBlob windows.DataBlob
	if len(ciphertext) > 0 {
		inBlob.Data = &ciphertext[0]
		inBlob.Size = uint32(len(ciphertext))
	}
	var outBlob windows.DataBlob
	err := windows.CryptUnprotectData(&inBlob, nil, nil, 0, nil, cryptProtectUIForbidden, &outBlob)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))
	out := make([]byte, outBlob.Size)
	if outBlob.Size > 0 {
		copy(out, unsafe.Slice(outBlob.Data, outBlob.Size))
	}
	return out, nil
}

func runIcacls(t testing.TB, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "icacls.exe", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// ── CLI Subprocess & Go Toolchain Helpers ───────────────────────────────────

func binPath(t testing.TB, name string) string {
	t.Helper()
	p := filepath.Join(srcRoot(t), "bin", name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("binario esperado nao encontrado em %s: %v", p, err)
	}
	return p
}

func execCLI(t testing.TB, timeout time.Duration, stdinBytes []byte, binName string, args ...string) (int, string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath(t, binName), args...)
	if stdinBytes != nil {
		cmd.Stdin = bytes.NewReader(stdinBytes)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return exitCode, stdout.String(), stderr.String(), err
}

func runGoCmd(t testing.TB, timeout time.Duration, args ...string) (int, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = srcRoot(t)
	cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=1")
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return exitCode, string(out), err
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func makeFakeJWT(claims map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ed25519-nkey","typ":"JWT"}`))
	pl, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(pl)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fake-sig-bytes"))
	return hdr + "." + payload + "." + sig
}

func readJSONLine(t testing.TB, r *bufio.Reader, timeout time.Duration) map[string]any {
	t.Helper()
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		l, err := r.ReadString('\n')
		ch <- res{l, err}
	}()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("readJSONLine: %v", out.err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.line)), &m); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", out.line, err)
		}
		return m
	case <-time.After(timeout):
		t.Fatalf("timeout (%v) lendo JSON line do pipe", timeout)
		return nil
	}
}
