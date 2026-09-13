package netproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Proxy gerencia requisicoes HTTP reversas para dispositivos na rede local do agent.
type Proxy struct {
	allowlist *Allowlist
	client    *http.Client
	maxBytes  int64
}

// ProxyRequest representa uma requisicao do viewer.
type ProxyRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// ProxyResponse representa a resposta ao viewer.
type ProxyResponse struct {
	Success    bool              `json:"success"`
	Error      string            `json:"error,omitempty"`
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	ContentLen int64             `json:"contentLen"`
}

// NewProxy cria um novo proxy de rede.
func NewProxy(allowlist *Allowlist, maxBytes int64) *Proxy {
	p := &Proxy{
		allowlist: allowlist,
		maxBytes:  maxBytes,
	}
	p.client = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			// M7: cada salto de redirect é revalidado contra a allowlist —
			// antes o redirect podia apontar para qualquer host (SSRF pivot).
			nextHost, nextPort := extractHostPort(req.URL.String())
			if !allowlist.IsAllowed(nextHost, nextPort) {
				return fmt.Errorf("redirect bloqueado pela allowlist: %s:%d", nextHost, nextPort)
			}
			return nil
		},
		// M7: dialer que resolve e valida o IP efetivo ANTES de dialar — fecha
		// o TOCTOU em que a allowlist validava o hostname e o client re-resolvia
		// (DNS podia responder IP fora da allowlist na conexão real).
		Transport: &http.Transport{
			DialContext: p.dialAllowed,
		},
	}
	return p
}

// dialAllowed resolve o host do endereço e diala SOMENTE um IP validado na
// allowlist (M7: DNS TOCTOU). O hostname é preservado para Host header/SNI TLS.
func (p *Proxy) dialAllowed(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("endereco invalido %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("porta invalida %q", portStr)
	}
	if !p.allowlist.IsAllowed(host, port) {
		return nil, fmt.Errorf("acesso bloqueado pela allowlist: %s:%d", host, port)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("resolver %s: %v", host, err)
	}
	var dialer net.Dialer
	dialer.Timeout = 10 * time.Second
	for _, ipa := range ips {
		if !p.allowlist.ContainsIP(ipa.IP) {
			continue
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), portStr))
	}
	return nil, fmt.Errorf("nenhum IP de %s dentro da allowlist", host)
}

// HandleRequest processa uma requisicao de proxy do viewer.
func (p *Proxy) HandleRequest(raw []byte) []byte {
	var req ProxyRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return p.errorResponse("payload invalido: " + err.Error())
	}

	if req.URL == "" {
		return p.errorResponse("URL nao informada")
	}

	// Extrai host e porta da URL
	host, port := extractHostPort(req.URL)

	// Valida contra allowlist
	if !p.allowlist.IsAllowed(host, port) {
		return p.errorResponse(fmt.Sprintf("acesso bloqueado pela allowlist: %s:%d", host, port))
	}

	// Executa request HTTP
	resp, err := p.doRequest(req)
	if err != nil {
		return p.errorResponse("requisicao proxy: " + err.Error())
	}

	return resp
}

func (p *Proxy) doRequest(req ProxyRequest) ([]byte, error) {
	httpReq, err := http.NewRequest(req.Method, req.URL, strings.NewReader(req.Body))
	if err != nil {
		return nil, fmt.Errorf("criar request: %w", err)
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if httpReq.Header.Get("User-Agent") == "" {
		httpReq.Header.Set("User-Agent", "DiscoveryRMM-NetProxy/1.0")
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executar request: %w", err)
	}
	defer resp.Body.Close()

	// Limita tamanho da resposta
	limitedReader := io.LimitReader(resp.Body, p.maxBytes)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("ler resposta: %w", err)
	}

	headers := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	result := ProxyResponse{
		Success:    true,
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       string(bodyBytes),
		ContentLen: int64(len(bodyBytes)),
	}
	b, _ := json.Marshal(result)
	return b, nil
}

func (p *Proxy) errorResponse(msg string) []byte {
	r := ProxyResponse{Success: false, Error: msg}
	b, _ := json.Marshal(r)
	return b
}

func extractHostPort(rawURL string) (host string, port int) {
	port = 80
	urlStr := strings.TrimSpace(rawURL)

	// Remove scheme
	if strings.HasPrefix(urlStr, "https://") {
		urlStr = strings.TrimPrefix(urlStr, "https://")
		port = 443
	} else if strings.HasPrefix(urlStr, "http://") {
		urlStr = strings.TrimPrefix(urlStr, "http://")
	}

	// Remove path
	if idx := strings.Index(urlStr, "/"); idx >= 0 {
		urlStr = urlStr[:idx]
	}

	// Split host:port
	if idx := strings.LastIndex(urlStr, ":"); idx > 0 {
		host = urlStr[:idx]
		portStr := urlStr[idx+1:]
		if p, err := fmt.Sscanf(portStr, "%d", &port); err == nil && p == 1 {
			return
		}
		port = 80
	} else {
		host = urlStr
	}

	return
}

// Ensure imports
var _ = http.StatusOK
var _ = io.EOF
