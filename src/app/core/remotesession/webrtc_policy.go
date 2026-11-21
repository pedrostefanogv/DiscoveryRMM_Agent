package remotesession

import (
	"strings"
)

// WebRtcPolicy é a política de WebRTC que o servidor envia no bloco "webrtc" do
// payload de start. Espelha RemoteAccessWebRtcPolicy do backend.
//
// O agente é o PONTO DE COERÇÃO: o viewer é não confiável e o servidor está fora
// do caminho dos dados (a mídia é peer-to-peer). Por isso todo campo é
// revalidado aqui — nunca se assume que a API já validou.
//
// FAIL-CLOSED: a ausência do bloco, um tipo errado ou um valor fora de faixa
// resultam em Enabled=false, isto é, o caminho NATS — que é o comportamento
// anterior e o único validado em produção.
type WebRtcPolicy struct {
	// Enabled indica que o servidor autorizou WebRTC para ESTA sessão. Já vem
	// considerando o kill switch e a lista de kinds do lado do servidor.
	Enabled bool

	// Kinds autorizados (tela, rdp, arquivos). Informativo no agente: o kind da
	// sessão já foi decidido no start, mas manter a lista permite recusar uma
	// combinação inconsistente em vez de abrir um peer connection inútil.
	Kinds []string

	// IceServers no formato do wire, já normalizado para slices de URL.
	IceServers []IceServer

	// RelayOnly força todo o tráfego pelo TURN. Exige ao menos um TURN na lista;
	// sem ele a política é recusada (ver Validate).
	RelayOnly bool

	// FallbackToNats decide o que fazer quando o ICE falha. Default true: o
	// operador nunca deve ficar sem sessão por causa de uma otimização.
	FallbackToNats bool

	// Timeouts do ICE em segundos, afinados para suporte remoto (detectar a
	// falha rápido e cair para NATS).
	DisconnectedTimeoutSeconds int
	FailedTimeoutSeconds       int
	KeepAliveIntervalSeconds   int
}

// IceServer é um servidor ICE no formato aceito pelo stack de WebRTC.
//
// Urls é sempre um slice porque os dois consumidores (o agente e o navegador)
// aceitam array; o servidor também emite array. A leitura, porém, TOLERA string,
// pelo mesmo motivo que o Breeze tolera: uma mudança de serialização no console
// não pode quebrar a negociação em silêncio.
type IceServer struct {
	Urls       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Defaults dos timeouts de ICE. Espelham o value object do backend
// (WebRtcIceOptions) e são DIFERENTES dos defaults do pion (25 s de failed), que
// são calibrados para videochamada e deixariam o operador esperando 25 s antes
// de o fallback para NATS entrar.
const (
	defaultWebRtcDisconnectedTimeoutSeconds = 5
	defaultWebRtcFailedTimeoutSeconds       = 15
	defaultWebRtcKeepAliveIntervalSeconds   = 2
)

// disabledWebRtcPolicy é o resultado de qualquer falha de leitura: WebRTC
// desligado, caminho NATS.
func disabledWebRtcPolicy() WebRtcPolicy {
	return WebRtcPolicy{
		Enabled:                    false,
		FallbackToNats:             true,
		DisconnectedTimeoutSeconds: defaultWebRtcDisconnectedTimeoutSeconds,
		FailedTimeoutSeconds:       defaultWebRtcFailedTimeoutSeconds,
		KeepAliveIntervalSeconds:   defaultWebRtcKeepAliveIntervalSeconds,
	}
}

// ParseWebRtcPolicy lê o bloco "webrtc" do payload de start.
//
// Leitura TOLERANTE no sentido de nunca entrar em pânico nem rejeitar a sessão
// inteira por um bloco malformado: um bloco inválido vira WebRTC desligado e a
// sessão continua pelo NATS. Rejeitar o start seria pior — o operador perderia o
// acesso por causa de uma otimização de transporte.
func ParseWebRtcPolicy(raw any) WebRtcPolicy {
	block, ok := raw.(map[string]any)
	if !ok || block == nil {
		return disabledWebRtcPolicy()
	}

	// enabled precisa ser booleano de verdade. Um valor ausente ou de outro tipo
	// NÃO liga o WebRTC.
	enabled, ok := strictBool(block["enabled"])
	if !ok || !enabled {
		policy := disabledWebRtcPolicy()
		// Preserva os campos que ainda servem para diagnóstico/UI, mas com
		// Enabled=false o runtime não abre peer connection.
		policy.Kinds = parseStringList(block["kinds"])
		policy.IceServers = parseIceServers(block["iceServers"])
		if relayOnly, ok := strictBool(block["relayOnly"]); ok {
			policy.RelayOnly = relayOnly
		}
		if fallback, ok := strictBool(block["fallbackToNats"]); ok {
			policy.FallbackToNats = fallback
		}
		return policy
	}

	relayOnly, _ := strictBool(block["relayOnly"])

	policy := WebRtcPolicy{
		Enabled:        true,
		Kinds:          parseStringList(block["kinds"]),
		IceServers:     parseIceServers(block["iceServers"]),
		RelayOnly:      relayOnly,
		FallbackToNats: true,
		// Timeouts SEMPRE pelos defaults do agente quando ausentes: confiar no
		// default do pion deixaria 25 s de espera antes do fallback.
		DisconnectedTimeoutSeconds: defaultWebRtcDisconnectedTimeoutSeconds,
		FailedTimeoutSeconds:       defaultWebRtcFailedTimeoutSeconds,
		KeepAliveIntervalSeconds:   defaultWebRtcKeepAliveIntervalSeconds,
	}

	// Um "false" explícito desliga o fallback; ausente ou tipo errado mantém true.
	if fallback, ok := strictBool(block["fallbackToNats"]); ok {
		policy.FallbackToNats = fallback
	}

	policy.DisconnectedTimeoutSeconds = toIntInRange(
		block["disconnectedTimeoutSeconds"], 1, 120, defaultWebRtcDisconnectedTimeoutSeconds)
	policy.FailedTimeoutSeconds = toIntInRange(
		block["failedTimeoutSeconds"], 1, 120, defaultWebRtcFailedTimeoutSeconds)
	policy.KeepAliveIntervalSeconds = toIntInRange(
		block["keepAliveIntervalSeconds"], 1, 120, defaultWebRtcKeepAliveIntervalSeconds)

	return policy
}

// Validate confere a política já parseada e devolve a razão da recusa, ou "" se
// estiver utilizável.
//
// O caso central é RelayOnly sem TURN: aceitar isso produz uma sessão que NUNCA
// conecta (não há candidato relay e o ICE falha no timeout), com o operador
// vendo "conectando..." sem causa. A API já recusa, mas o agente revalida porque
// é ele o ponto de coerção.
func (p WebRtcPolicy) Validate() string {
	if !p.Enabled {
		return ""
	}

	if p.RelayOnly && !p.HasTurn() {
		return "relayOnly=true sem servidor TURN: a sessão nunca estabeleceria (não há candidato relay)"
	}

	if len(p.IceServers) == 0 {
		return "nenhum servidor ICE utilizável no bloco webrtc"
	}

	return ""
}

// HasTurn indica se há ao menos um servidor TURN utilizável.
func (p WebRtcPolicy) HasTurn() bool {
	for _, server := range p.IceServers {
		for _, url := range server.Urls {
			lower := strings.ToLower(url)
			if strings.HasPrefix(lower, "turn:") || strings.HasPrefix(lower, "turns:") {
				return true
			}
		}
	}
	return false
}

// HasStun indica se há ao menos um servidor STUN utilizável.
func (p WebRtcPolicy) HasStun() bool {
	for _, server := range p.IceServers {
		for _, url := range server.Urls {
			if strings.HasPrefix(strings.ToLower(url), "stun:") {
				return true
			}
		}
	}
	return false
}

// AllowsKind indica se o servidor autorizou WebRTC para este tipo de sessão.
//
// Existe como defesa em profundidade: mesmo que a API envie o bloco por engano
// numa sessão de terminal, o agente recusa o caminho WebRTC — que ali não traz
// ganho algum e só adicionaria um caminho de falha.
func (p WebRtcPolicy) AllowsKind(kind string) bool {
	if !p.Enabled {
		return false
	}

	// Lista vazia = nenhum kind autorizado (nunca "todos": um default implícito
	// ligaria WebRTC em terminal).
	if len(p.Kinds) == 0 {
		return false
	}

	target := strings.ToLower(strings.TrimSpace(kind))
	for _, k := range p.Kinds {
		normalized := strings.ToLower(strings.TrimSpace(k))
		if normalized == target {
			return true
		}
		// "all" cobre a sessão composta, que inclui tela.
		if normalized == "all" && target == "all" {
			return true
		}
	}
	return false
}

// parseIceServers lê o array de servidores ICE, tolerando urls como string OU
// array, e descartando entradas sem URL utilizável.
func parseIceServers(raw any) []IceServer {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}

	servers := make([]IceServer, 0, len(list))
	for _, item := range list {
		block, ok := item.(map[string]any)
		if !ok || block == nil {
			continue
		}

		urls := parseUrls(block["urls"])
		if len(urls) == 0 {
			continue
		}

		servers = append(servers, IceServer{
			Urls:       urls,
			Username:   toString(block["username"]),
			Credential: toString(block["credential"]),
		})
	}

	return servers
}

// parseUrls aceita string única ou array de strings, e descarta o que não for
// URL de ICE com esquema reconhecido.
func parseUrls(raw any) []string {
	var candidates []string

	switch value := raw.(type) {
	case string:
		candidates = []string{value}
	case []any:
		for _, item := range value {
			if s, ok := item.(string); ok {
				candidates = append(candidates, s)
			}
		}
	default:
		return nil
	}

	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))

	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if !isIceUrl(trimmed) {
			continue
		}
		if _, dup := seen[trimmed]; dup {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}

	return out
}

// isIceUrl exige esquema reconhecido E host após ele. Um endereço sem esquema
// ("stun.l.google.com:19302") é o erro mais comum de quem digita à mão, e
// passaria silenciosamente para o stack de ICE onde falharia sem explicação.
func isIceUrl(url string) bool {
	lower := strings.ToLower(url)
	for _, prefix := range []string{"stun:", "turn:", "turns:"} {
		if strings.HasPrefix(lower, prefix) {
			return len(strings.TrimSpace(url)) > len(prefix)
		}
	}
	return false
}

func parseStringList(raw any) []string {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				out = append(out, strings.ToLower(trimmed))
			}
		}
	}
	return out
}

// strictBool exige um booleano DE VERDADE.
//
// Não reusa o toBool do pacote (consent.go) de propósito: aquele aceita a string
// "true" e float64 != 0. Para o consentimento isso é conveniente, mas aqui seria
// uma porta de entrada: um "enabled": "true" vindo de um viewer adulterado
// ligaria o transporte P2P. Nesta política, qualquer coisa que não seja o literal
// booleano é tratada como ausente.
func strictBool(value any) (bool, bool) {
	b, ok := value.(bool)
	return b, ok
}

// toIntInRange converte e valida a faixa: um valor fora dela volta ao default em
// vez de ser aceito (proteger o runtime de um 0 que desligaria o timeout).
func toIntInRange(v any, min, max, fallback int) int {
	value := toInt(v, fallback)
	if value < min || value > max {
		return fallback
	}
	return value
}
