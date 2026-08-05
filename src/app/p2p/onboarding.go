package p2p

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"discovery/app/p2pmeta"
)

// OnboardingCredentials representa as credenciais extraídas da resposta
// de registro zero-touch.
type OnboardingCredentials struct {
	AuthToken string
	AgentID   string
	ApiScheme string
	ApiServer string
	ClientID  string
	SiteID    string
}

// ComputeOnboardingSignature builds the HMAC-SHA256 for an onboarding offer.
// Key = deployKey itself (self-contained; no out-of-band shared secret needed).
func ComputeOnboardingSignature(sourceAgent, serverURL, deployKey, expiresAt, nonce string) string {
	payload := strings.Join([]string{sourceAgent, serverURL, deployKey, expiresAt, nonce}, "\n")
	mac := hmac.New(sha256.New, []byte(deployKey))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// BuildOnboardingOffer creates a signed onboarding offer for distribution to
// unconfigured peers.
func BuildOnboardingOffer(sourceAgentID, serverURL, deployKey string, ttl time.Duration) (p2pmeta.OnboardingRequest, error) {
	if strings.TrimSpace(deployKey) == "" {
		return p2pmeta.OnboardingRequest{}, fmt.Errorf("deployKey vazio: impossivel assinar oferta")
	}
	if strings.TrimSpace(serverURL) == "" {
		return p2pmeta.OnboardingRequest{}, fmt.Errorf("serverURL vazio: impossivel montar oferta")
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return p2pmeta.OnboardingRequest{}, err
	}
	nonce := hex.EncodeToString(nonceBytes)
	expiresAt := time.Now().UTC().Add(ttl).Format(time.RFC3339)
	sig := ComputeOnboardingSignature(sourceAgentID, serverURL, deployKey, expiresAt, nonce)
	return p2pmeta.OnboardingRequest{
		ServerURL:    serverURL,
		DeployKey:    deployKey,
		ExpiresAtUTC: expiresAt,
		SourceAgent:  sourceAgentID,
		Nonce:        nonce,
		Signature:    sig,
	}, nil
}

// ValidateServerURL ensures the URL uses http or https and has a non-empty host,
// preventing SSRF via unexpected schemes (file://, data://, etc.).
func ValidateServerURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("URL do servidor invalida: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL do servidor deve usar http ou https, obtido: %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL do servidor sem host")
	}
	return nil
}

// BuildZeroTouchServerURL monta <scheme>://<server> normalizando o scheme.
func BuildZeroTouchServerURL(scheme, server string) string {
	scheme = strings.TrimSpace(strings.ToLower(scheme))
	if scheme == "" {
		scheme = "https"
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return ""
	}
	return scheme + "://" + server
}

// ParseZeroTouchServerURL separa scheme e host de uma server URL.
func ParseZeroTouchServerURL(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("server url vazio")
	}
	input := raw
	if !strings.Contains(input, "://") {
		input = "https://" + input
	}
	u, err := url.Parse(input)
	if err != nil {
		return "", "", err
	}
	scheme := strings.TrimSpace(strings.ToLower(u.Scheme))
	if scheme == "" {
		scheme = "https"
	}
	if scheme != "http" && scheme != "https" {
		return "", "", fmt.Errorf("scheme invalido: %s", scheme)
	}
	host := strings.TrimSpace(u.Host)
	if host == "" {
		host = strings.Trim(strings.TrimSpace(u.Path), "/")
	}
	if host == "" {
		return "", "", fmt.Errorf("host ausente em server url")
	}
	return scheme, host, nil
}

func firstNonEmptyAnyString(values ...any) string {
	for _, value := range values {
		s := strings.TrimSpace(fmt.Sprint(value))
		if s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}

// FirstNonEmptyAnyString retorna o primeiro valor não-vazio (stringified).
func FirstNonEmptyAnyString(values ...any) string {
	return firstNonEmptyAnyString(values...)
}

// ParseZeroTouchRegisterResponse extrai credenciais da resposta JSON do
// endpoint de registro, aceitando múltiplos formatos de campo.
func ParseZeroTouchRegisterResponse(body []byte, fallbackServerURL string) (OnboardingCredentials, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return OnboardingCredentials{}, fmt.Errorf("resposta JSON invalida no registro zero-touch: %w", err)
	}

	extract := func(m map[string]any) OnboardingCredentials {
		credentials := OnboardingCredentials{
			AuthToken: firstNonEmptyAnyString(m["token"], m["authToken"], m["auth_token"], m["accessToken"], m["access_token"]),
			AgentID:   firstNonEmptyAnyString(m["agentId"], m["agentID"], m["agent_id"], m["id"]),
			ApiScheme: strings.ToLower(strings.TrimSpace(firstNonEmptyAnyString(m["apiScheme"], m["api_scheme"], m["scheme"]))),
			ApiServer: strings.TrimSpace(firstNonEmptyAnyString(m["apiServer"], m["api_server"], m["server"], m["serverHost"], m["server_host"])),
			ClientID:  strings.TrimSpace(firstNonEmptyAnyString(m["clientId"], m["client_id"], m["client"])),
			SiteID:    strings.TrimSpace(firstNonEmptyAnyString(m["siteId"], m["site_id"], m["site"])),
		}

		serverURL := strings.TrimSpace(firstNonEmptyAnyString(m["serverUrl"], m["server_url"], m["baseUrl"], m["base_url"]))
		if credentials.ApiScheme == "" || credentials.ApiServer == "" {
			if parsedScheme, parsedServer, err := ParseZeroTouchServerURL(serverURL); err == nil {
				if credentials.ApiScheme == "" {
					credentials.ApiScheme = parsedScheme
				}
				if credentials.ApiServer == "" {
					credentials.ApiServer = parsedServer
				}
			}
		}

		return credentials
	}

	mergeMissing := func(dst *OnboardingCredentials, src OnboardingCredentials) {
		if strings.TrimSpace(dst.AuthToken) == "" {
			dst.AuthToken = strings.TrimSpace(src.AuthToken)
		}
		if strings.TrimSpace(dst.AgentID) == "" {
			dst.AgentID = strings.TrimSpace(src.AgentID)
		}
		if strings.TrimSpace(dst.ApiScheme) == "" {
			dst.ApiScheme = strings.TrimSpace(src.ApiScheme)
		}
		if strings.TrimSpace(dst.ApiServer) == "" {
			dst.ApiServer = strings.TrimSpace(src.ApiServer)
		}
		if strings.TrimSpace(dst.ClientID) == "" {
			dst.ClientID = strings.TrimSpace(src.ClientID)
		}
		if strings.TrimSpace(dst.SiteID) == "" {
			dst.SiteID = strings.TrimSpace(src.SiteID)
		}
	}

	credentials := extract(raw)
	for _, key := range []string{"data", "result", "payload"} {
		nested, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		mergeMissing(&credentials, extract(nested))
	}

	credentials.AuthToken = strings.TrimSpace(credentials.AuthToken)
	credentials.AgentID = strings.TrimSpace(credentials.AgentID)
	credentials.ApiScheme = strings.TrimSpace(strings.ToLower(credentials.ApiScheme))
	credentials.ApiServer = strings.TrimSpace(credentials.ApiServer)

	if credentials.AuthToken == "" || credentials.AgentID == "" {
		return OnboardingCredentials{}, fmt.Errorf("resposta sem auth token/agent id no registro zero-touch")
	}

	if credentials.ApiScheme == "" || credentials.ApiServer == "" {
		parsedScheme, parsedServer, err := ParseZeroTouchServerURL(fallbackServerURL)
		if err != nil {
			return OnboardingCredentials{}, fmt.Errorf("resposta sem apiScheme/apiServer e fallback invalido: %w", err)
		}
		if credentials.ApiScheme == "" {
			credentials.ApiScheme = parsedScheme
		}
		if credentials.ApiServer == "" {
			credentials.ApiServer = parsedServer
		}
	}

	if credentials.ApiScheme != "http" && credentials.ApiScheme != "https" {
		return OnboardingCredentials{}, fmt.Errorf("apiScheme invalido na resposta do registro zero-touch")
	}

	if strings.TrimSpace(credentials.ApiServer) == "" {
		return OnboardingCredentials{}, fmt.Errorf("apiServer vazio na resposta do registro zero-touch")
	}

	return credentials, nil
}
