package netproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	return &Proxy{
		allowlist: allowlist,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		maxBytes: maxBytes,
	}
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

func (p *Proxy) doRequest(req ProxyRequest) []byte {
	httpReq, err := http.NewRequest(req.Method, req.URL, strings.NewReader(req.Body))
	if err != nil {
		return p.errorResponse("criar request: " + err.Error())
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if httpReq.Header.Get("User-Agent") == "" {
		httpReq.Header.Set("User-Agent", "DiscoveryRMM-NetProxy/1.0")
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return p.errorResponse("executar request: " + err.Error())
	}
	defer resp.Body.Close()

	// Limita tamanho da resposta
	limitedReader := io.LimitReader(resp.Body, p.maxBytes)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return p.errorResponse("ler resposta: " + err.Error())
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
	return b
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
