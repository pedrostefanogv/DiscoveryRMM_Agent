package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	p2pControlHeaderSourceAgent = "X-P2P-Source-Agent"
	p2pControlHeaderTimestamp   = "X-P2P-Control-Timestamp"
	p2pControlHeaderSignature   = "X-P2P-Control-Signature"
	p2pControlMaxSkew           = 5 * time.Minute
)

type p2pTransferServer struct {
	app *App

	mu           sync.RWMutex
	secret       []byte
	server       *http.Server
	listener     net.Listener
	baseURL      string
	tempDir      string
	agentID      string
	tokenTTL     time.Duration
	sharedSecret []byte
	peerSnapshot func() []P2PPeerView
}

type p2pTokenPayload struct {
	Artifact string `json:"a"`
	PeerID   string `json:"p"`
	Exp      int64  `json:"e"`
}

func newP2PTransferServer(app *App) *p2pTransferServer {
	return &p2pTransferServer{app: app}
}

func (s *p2pTransferServer) Start(ctx context.Context, cfg P2PConfig, agentID, tempDir string, peerSnapshot func() []P2PPeerView) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.server != nil {
		return nil
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}

	ln, port, err := listenInRange(cfg.HTTPListenPortRangeStart, cfg.HTTPListenPortRangeEnd)
	if err != nil {
		return err
	}

	host := detectLocalAddressForPeers()
	if host == "" {
		host = "127.0.0.1"
	}
	baseURL := fmt.Sprintf("http://%s:%d", host, port)

	mux := http.NewServeMux()
	mux.HandleFunc("/p2p/artifact/", s.handleArtifact)
	mux.HandleFunc("/p2p/artifact/access", s.handleArtifactAccess)
	mux.HandleFunc("/p2p/peers", s.handlePeers)
	mux.HandleFunc("/p2p/replicate", s.handleReplicate)
	mux.HandleFunc("/p2p/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"agentId": s.agentID,
		})
	})

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	s.secret = secret
	s.server = httpServer
	s.listener = ln
	s.baseURL = baseURL
	s.tempDir = tempDir
	s.agentID = strings.TrimSpace(agentID)
	s.tokenTTL = time.Duration(cfg.AuthTokenRotationMinutes) * time.Minute
	if strings.TrimSpace(cfg.SharedSecret) != "" {
		s.sharedSecret = []byte(strings.TrimSpace(cfg.SharedSecret))
	}
	s.peerSnapshot = peerSnapshot

	go func() {
		<-ctx.Done()
		_ = httpServer.Close()
	}()
	go func() {
		_ = httpServer.Serve(ln)
	}()

	if s.app != nil {
		s.app.logs.append("[p2p] servidor HTTP local ativo em " + baseURL)
	}

	return nil
}

func (s *p2pTransferServer) BaseURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.baseURL
}

func (s *p2pTransferServer) BuildArtifactAccess(artifactName, targetPeerID string) (P2PArtifactAccess, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.server == nil || s.listener == nil {
		return P2PArtifactAccess{}, errors.New("servidor P2P HTTP nao iniciado")
	}

	artifactName = sanitizeArtifactName(artifactName)
	if artifactName == "" {
		return P2PArtifactAccess{}, errors.New("nome de artifact invalido")
	}

	path := filepath.Join(s.tempDir, artifactName)
	info, err := os.Stat(path)
	if err != nil {
		return P2PArtifactAccess{}, err
	}
	if info.IsDir() {
		return P2PArtifactAccess{}, errors.New("artifact invalido")
	}

	ttl := s.tokenTTL
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	expiresAt := time.Now().Add(ttl)
	token, err := s.issueTokenLocked(artifactName, strings.TrimSpace(targetPeerID), expiresAt)
	if err != nil {
		return P2PArtifactAccess{}, err
	}

	safeName := url.PathEscape(artifactName)
	safePeer := url.QueryEscape(strings.TrimSpace(targetPeerID))
	safeToken := url.QueryEscape(token)
	downloadURL := fmt.Sprintf("%s/p2p/artifact/%s?peer=%s&token=%s", strings.TrimRight(s.baseURL, "/"), safeName, safePeer, safeToken)

	checksum, err := computeFileSHA256(path)
	if err != nil {
		return P2PArtifactAccess{}, err
	}

	return P2PArtifactAccess{
		ArtifactName:   artifactName,
		URL:            downloadURL,
		ChecksumSHA256: checksum,
		SizeBytes:      info.Size(),
		ExpiresAtUTC:   expiresAt.UTC().Format(time.RFC3339),
	}, nil
}

func (s *p2pTransferServer) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/p2p/artifact/")
	name, err := url.PathUnescape(name)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name = sanitizeArtifactName(name)
	if name == "" {
		http.Error(w, "artifact invalido", http.StatusBadRequest)
		return
	}

	peerID := strings.TrimSpace(r.URL.Query().Get("peer"))
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if err := s.verifyToken(name, peerID, token, time.Now()); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	path := filepath.Join(s.tempDir, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	checksum, _ := computeFileSHA256(path)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Artifact-SHA256", checksum)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	if s.app != nil && s.app.p2pCoord != nil {
		s.app.p2pCoord.recordBytesServed(info.Size())
	}
	http.ServeFile(w, r, path)
}

func (s *p2pTransferServer) handlePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")

	peers := []P2PPeerView{}
	s.mu.RLock()
	if s.peerSnapshot != nil {
		peers = s.peerSnapshot()
	}
	agentID := s.agentID
	s.mu.RUnlock()
	artifacts := s.localArtifactsSnapshot()

	_ = json.NewEncoder(w).Encode(map[string]any{
		"agentId":       agentID,
		"knownPeers":    peers,
		"artifacts":     artifacts,
		"catalogSource": "self",
		"updatedAtUtc":  time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *p2pTransferServer) handleArtifactAccess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ArtifactName string `json:"artifactName"`
		RequesterID  string `json:"requesterId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload invalido", http.StatusBadRequest)
		return
	}

	req.ArtifactName = sanitizeArtifactName(req.ArtifactName)
	req.RequesterID = strings.TrimSpace(req.RequesterID)
	if req.ArtifactName == "" {
		http.Error(w, "artifact invalido", http.StatusBadRequest)
		return
	}
	if req.RequesterID == "" {
		req.RequesterID = "peer-anon"
	}

	access, err := s.BuildArtifactAccess(req.ArtifactName, req.RequesterID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(access)
}

func (s *p2pTransferServer) localArtifactsSnapshot() []P2PArtifactView {
	if s.app == nil || s.app.p2pCoord == nil {
		return []P2PArtifactView{}
	}
	artifacts, err := s.app.p2pCoord.ListArtifacts()
	if err != nil {
		return []P2PArtifactView{}
	}
	return artifacts
}

func (s *p2pTransferServer) handleReplicate(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "modo push desabilitado: use transferencia pull sob demanda", http.StatusGone)
}

func (s *p2pTransferServer) downloadArtifact(access P2PArtifactAccess) (string, int64, error) {
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Get(access.URL)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", 0, fmt.Errorf("download remoto falhou HTTP %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if err := os.MkdirAll(s.tempDir, 0o755); err != nil {
		return "", 0, err
	}
	targetPath := filepath.Join(s.tempDir, access.ArtifactName)
	tmpPath := targetPath + ".partial"
	file, err := os.Create(tmpPath)
	if err != nil {
		return "", 0, err
	}
	size, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, closeErr
	}
	checksum, err := computeFileSHA256(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	if strings.TrimSpace(access.ChecksumSHA256) != "" && !strings.EqualFold(strings.TrimSpace(access.ChecksumSHA256), checksum) {
		_ = os.Remove(tmpPath)
		return "", 0, fmt.Errorf("checksum divergente")
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, err
	}
	return targetPath, size, nil
}

func (s *p2pTransferServer) BuildReplicationHeaders(sourceAgentID string, access P2PArtifactAccess) map[string]string {
	headers := map[string]string{
		p2pControlHeaderSourceAgent: strings.TrimSpace(sourceAgentID),
	}
	s.mu.RLock()
	secret := s.sharedSecret
	s.mu.RUnlock()
	if len(secret) == 0 {
		return headers
	}
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	headers[p2pControlHeaderTimestamp] = timestamp
	headers[p2pControlHeaderSignature] = signReplicationControl(secret, strings.TrimSpace(sourceAgentID), access, timestamp)
	return headers
}

func (s *p2pTransferServer) verifyReplicationControl(r *http.Request, access P2PArtifactAccess) error {
	s.mu.RLock()
	secret := s.sharedSecret
	s.mu.RUnlock()
	if len(secret) == 0 {
		return nil
	}
	sourceAgent := strings.TrimSpace(r.Header.Get(p2pControlHeaderSourceAgent))
	timestamp := strings.TrimSpace(r.Header.Get(p2pControlHeaderTimestamp))
	signature := strings.TrimSpace(r.Header.Get(p2pControlHeaderSignature))
	if sourceAgent == "" || timestamp == "" || signature == "" {
		return errors.New("controle de replicacao sem autenticacao")
	}
	tsUnix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("timestamp invalido")
	}
	when := time.Unix(tsUnix, 0)
	if time.Since(when) > p2pControlMaxSkew || time.Until(when) > p2pControlMaxSkew {
		return errors.New("timestamp fora da janela permitida")
	}
	expected := signReplicationControl(secret, sourceAgent, access, timestamp)
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return errors.New("assinatura de controle invalida")
	}
	return nil
}

func signReplicationControl(secret []byte, sourceAgentID string, access P2PArtifactAccess, timestamp string) string {
	payload := strings.Join([]string{
		strings.TrimSpace(sourceAgentID),
		strings.TrimSpace(access.ArtifactName),
		strings.TrimSpace(access.ChecksumSHA256),
		strings.TrimSpace(timestamp),
	}, "\n")
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *p2pTransferServer) issueTokenLocked(artifactName, peerID string, expiresAt time.Time) (string, error) {
	payload := p2pTokenPayload{
		Artifact: artifactName,
		PeerID:   strings.TrimSpace(peerID),
		Exp:      expiresAt.Unix(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	bodyEncoded := base64.RawURLEncoding.EncodeToString(body)

	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(bodyEncoded))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return bodyEncoded + "." + sig, nil
}

func (s *p2pTransferServer) verifyToken(artifactName, peerID, token string, now time.Time) error {
	s.mu.RLock()
	secret := s.secret
	s.mu.RUnlock()

	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return errors.New("token invalido")
	}
	bodyEncoded := parts[0]
	receivedSig := parts[1]

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(bodyEncoded))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expectedSig), []byte(receivedSig)) {
		return errors.New("assinatura invalida")
	}

	body, err := base64.RawURLEncoding.DecodeString(bodyEncoded)
	if err != nil {
		return err
	}

	var payload p2pTokenPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if payload.Artifact != artifactName {
		return errors.New("artifact nao confere")
	}
	if strings.TrimSpace(payload.PeerID) != "" && strings.TrimSpace(payload.PeerID) != strings.TrimSpace(peerID) {
		return errors.New("peer nao autorizado")
	}
	if now.Unix() > payload.Exp {
		return errors.New("token expirado")
	}
	return nil
}

func listenInRange(start, end int) (net.Listener, int, error) {
	if start <= 0 || end <= 0 || start > end {
		return nil, 0, errors.New("range de portas invalida")
	}
	for port := start; port <= end; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, fmt.Errorf("nao foi possivel abrir porta no range %d-%d", start, end)
}

func sanitizeArtifactName(name string) string {
	name = strings.TrimSpace(name)
	if strings.Contains(name, "..") {
		return ""
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return ""
	}
	name = filepath.Base(name)
	if name == "." || name == "" {
		return ""
	}
	return name
}

func computeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func detectLocalAddressForPeers() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || localAddr.IP == nil {
		return ""
	}
	return localAddr.IP.String()
}
