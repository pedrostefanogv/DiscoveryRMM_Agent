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
	r.logf("[acl][nats] claims do AgentIdentity validadas para 7 subscribes e 4 publishes")
	return nil
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
	}
	expectedPub := []string{
		subjects.Heartbeat,
		subjects.Result,
		subjects.Hardware,
		subjects.RemoteDebugLog,
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

func compareSubjectSets(direction string, got []string, expected []string) error {
	gotList := normalizeSubjectList(got)
	expectedList := normalizeSubjectList(expected)

	gotSet := make(map[string]struct{}, len(gotList))
	for _, item := range gotList {
		gotSet[item] = struct{}{}
	}
	expectedSet := make(map[string]struct{}, len(expectedList))
	for _, item := range expectedList {
		expectedSet[item] = struct{}{}
	}

	var missing []string
	for _, item := range expectedList {
		if _, ok := gotSet[item]; !ok {
			missing = append(missing, item)
		}
	}
	var extra []string
	for _, item := range gotList {
		if _, ok := expectedSet[item]; !ok {
			extra = append(extra, item)
		}
	}

	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Errorf("subjects de %s divergentes (missing=%v extra=%v)", direction, missing, extra)
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
