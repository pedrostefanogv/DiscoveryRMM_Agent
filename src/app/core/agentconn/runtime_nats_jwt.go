package agentconn

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func (r *Runtime) validateAgentIdentityACL(subjects natsSubjects, jwtCandidate string) error {
	jwt := extractJWTToken(jwtCandidate)
	if jwt == "" {
		r.logf("[acl][nats] JWT indisponivel para validacao local dos subjects do AgentIdentity")
		return nil
	}
	if err := validateAgentIdentityJWTClaims(jwt, subjects); err != nil {
		return err
	}
	r.warnLegacyWildcardJetStreamACL(jwt, subjects.ClientID)
	r.logf("[acl][nats] claims do AgentIdentity validadas para 7 subscribes e 4 publishes")
	return nil
}

func expectedTenantJetStreamPubSubjects(clientID string) []string {
	cid := normalizeCompactGUID(clientID)
	if cid == "" {
		return nil
	}
	fanoutStream := fanoutStreamPrefix + "_" + cid
	supportStream := supportChatStreamPrefix + "_" + cid
	return []string{
		fmt.Sprintf("$JS.API.STREAM.INFO.%s", fanoutStream),
		fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.>", fanoutStream),
		fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.>", fanoutStream),
		fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.%s.>", fanoutStream),
		fmt.Sprintf("$JS.ACK.%s.>", fanoutStream),
		fmt.Sprintf("$JS.API.STREAM.INFO.%s", fanoutGlobalStreamName),
		fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.>", fanoutGlobalStreamName),
		fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.>", fanoutGlobalStreamName),
		fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.%s.>", fanoutGlobalStreamName),
		fmt.Sprintf("$JS.ACK.%s.>", fanoutGlobalStreamName),
		fmt.Sprintf("$JS.API.STREAM.INFO.%s", supportStream),
		fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.>", supportStream),
		fmt.Sprintf("$JS.API.CONSUMER.CREATE.%s.>", supportStream),
		fmt.Sprintf("$JS.API.CONSUMER.MSG.NEXT.%s.>", supportStream),
		fmt.Sprintf("$JS.ACK.%s.>", supportStream),
	}
}

func isLegacyWildcardJetStreamSubject(subj string) bool {
	s := strings.TrimSpace(subj)
	return strings.Contains(s, "."+fanoutStreamPrefix+".>") ||
		strings.Contains(s, "."+supportChatStreamPrefix+".>")
}

func (r *Runtime) warnLegacyWildcardJetStreamACL(jwt, clientID string) {
	claims, err := decodeJWTClaimsUnverified(jwt)
	if err != nil {
		return
	}
	pubAllow, err := extractJWTAllowedSubjects(claims, "pub")
	if err != nil {
		return
	}
	for _, s := range pubAllow {
		if isLegacyWildcardJetStreamSubject(s) {
			r.logf("[acl][nats] AVISO: JWT contem permissao JetStream global legada (%s) para clientId=%s", s, clientID)
			return
		}
	}
}

func validateAgentIdentityJWTClaims(jwt string, subjects natsSubjects) error {
	claims, err := decodeJWTClaimsUnverified(jwt)
	if err != nil {
		return err
	}
	subAllow, err := extractJWTAllowedSubjects(claims, "sub")
	if err != nil {
		return fmt.Errorf("claim sub.allow invalida: %w", err)
	}
	pubAllow, err := extractJWTAllowedSubjects(claims, "pub")
	if err != nil {
		return fmt.Errorf("claim pub.allow invalida: %w", err)
	}

	expectedSub := []string{
		subjects.CommandAgent,
		subjects.CommandSiteFanout,
		subjects.CommandClientFanout,
		subjects.CommandGlobalFanout,
		subjects.GlobalPong,
		subjects.SyncPing,
		subjects.P2PDiscovery,
		// Canal unico de controle do remote debug: o agente ASSINA o mesmo
		// subject em que publica (ping do viewer, setLevel do servidor).
		subjects.RemoteDebugControl,
		// Remote-session subjects use canonical UUIDs without hyphens.
		// O agent precisa SUBSCREVER os subjects de input/control/term.in/files.req/
		// proxy.req/signal para receber interação do viewer (mouse, teclado, terminal,
		// arquivos, proxy). Sem isso, o NATS bloqueia a entrega (Permissions Violation).
		fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.>",
			stripSubjectHyphens(subjects.ClientID),
			stripSubjectHyphens(subjects.SiteID),
			stripSubjectHyphens(subjects.AgentID)),
	}
	expectedPub := []string{
		subjects.Heartbeat,
		subjects.Result,
		subjects.Hardware,
		subjects.RemoteDebugLog,
		subjects.RemoteDebugControl,
		// Remote-session subjects use canonical UUIDs without hyphens.
		fmt.Sprintf("tenant.%s.site.%s.agent.%s.remote.session.>",
			stripSubjectHyphens(subjects.ClientID),
			stripSubjectHyphens(subjects.SiteID),
			stripSubjectHyphens(subjects.AgentID)),
	}

	if err := compareSubjectSets("subscribe", subAllow, expectedSub); err != nil {
		return err
	}
	if err := compareSubjectSets("publish", pubAllow, expectedPub); err != nil {
		return err
	}
	return nil
}

func decodeJWTClaimsUnverified(jwt string) (map[string]any, error) {
	parts := strings.Split(strings.TrimSpace(jwt), ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("token nao possui formato JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("falha ao decodificar payload JWT: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("claims JWT invalidas: %w", err)
	}
	return claims, nil
}

func extractJWTAllowedSubjects(claims map[string]any, direction string) ([]string, error) {
	natsClaims, ok := claims["nats"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("claim nats ausente")
	}
	dirClaim, ok := natsClaims[direction].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("claim nats.%s ausente", direction)
	}
	allowRaw, ok := dirClaim["allow"]
	if !ok {
		return nil, fmt.Errorf("claim nats.%s.allow ausente", direction)
	}

	var out []string
	switch typed := allowRaw.(type) {
	case []any:
		for _, value := range typed {
			s := strings.TrimSpace(toString(value))
			if s != "" {
				out = append(out, s)
			}
		}
	case []string:
		for _, value := range typed {
			s := strings.TrimSpace(value)
			if s != "" {
				out = append(out, s)
			}
		}
	case string:
		s := strings.TrimSpace(typed)
		if s != "" {
			out = append(out, s)
		}
	default:
		return nil, fmt.Errorf("claim nats.%s.allow com tipo invalido", direction)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("claim nats.%s.allow vazio", direction)
	}
	return out, nil
}

// compareSubjectSets exige que TODOS os subjects esperados estejam presentes
// nos claims (missing == 0), mas IGNORA subjects extras.
//
// Exigir igualdade EXATA era um defeito: o servidor concede legitimamente
// subjects adicionais ao agente (ex.: $JS.API.*/$JS.ACK.* do JetStream,
// _INBOX.> para request/reply e o canal do chat de suporte). Qualquer um deles
// aparecendo no JWT fazia esta validação devolver erro, o que abortava a
// SESSÃO NATS INTEIRA (runNATSSession → "ACL/JWT do AgentIdentity invalida").
// O efeito prático era o pior possível: o agente parava de receber QUALQUER
// comando, inclusive o de desinstalação, por causa de uma permissão A MAIS que
// não faz mal algum ao agente.
//
// A checagem de "missing" continua rígida: sem os subjects de comando o agente
// realmente não seria alcançado.
func compareSubjectSets(direction string, got []string, expected []string) error {
	gotList := normalizeSubjectList(got)
	expectedList := normalizeSubjectList(expected)

	gotSet := make(map[string]struct{}, len(gotList))
	for _, item := range gotList {
		gotSet[item] = struct{}{}
	}

	var missing []string
	for _, item := range expectedList {
		if _, ok := gotSet[item]; !ok {
			missing = append(missing, item)
		}
	}

	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("subjects de %s ausentes (missing=%v)", direction, missing)
}

func normalizeSubjectList(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized != "" {
			set[normalized] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func extractJWTToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Count(raw, ".") == 2 {
		return raw
	}
	return ""
}
