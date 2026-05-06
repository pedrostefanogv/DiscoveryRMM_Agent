package agentconn

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestResolveNATSSubjects_CanonicalLayout(t *testing.T) {
	subjects, err := resolveNATSSubjects(Config{
		ClientID: "client-1",
		SiteID:   "site-1",
		AgentID:  "agent-1",
	})
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	prefix := "tenant.client-1.site.site-1.agent.agent-1"
	if subjects.CommandAgent != prefix+".command" {
		t.Fatalf("CommandAgent = %q", subjects.CommandAgent)
	}
	if subjects.CommandSiteFanout != "tenant.client-1.site.site-1.agents.command" {
		t.Fatalf("CommandSiteFanout = %q", subjects.CommandSiteFanout)
	}
	if subjects.CommandClientFanout != "tenant.client-1.agents.command" {
		t.Fatalf("CommandClientFanout = %q", subjects.CommandClientFanout)
	}
	if subjects.CommandGlobalFanout != "tenant.global.agents.command" {
		t.Fatalf("CommandGlobalFanout = %q", subjects.CommandGlobalFanout)
	}
	if subjects.GlobalPong != "tenant.global.pong" {
		t.Fatalf("GlobalPong = %q", subjects.GlobalPong)
	}
	if subjects.Heartbeat != prefix+".heartbeat" {
		t.Fatalf("Heartbeat = %q", subjects.Heartbeat)
	}
	if subjects.Result != prefix+".result" {
		t.Fatalf("Result = %q", subjects.Result)
	}
	if subjects.Hardware != prefix+".hardware" {
		t.Fatalf("Hardware = %q", subjects.Hardware)
	}
	if subjects.RemoteDebugLog != prefix+".remote-debug.log" {
		t.Fatalf("RemoteDebugLog = %q", subjects.RemoteDebugLog)
	}
	if subjects.SyncPing != prefix+".sync.ping" {
		t.Fatalf("SyncPing = %q", subjects.SyncPing)
	}
	if subjects.P2PDiscovery != "tenant.client-1.site.site-1.p2p.discovery" {
		t.Fatalf("P2PDiscovery = %q", subjects.P2PDiscovery)
	}
}

func TestParseP2PDiscoverySnapshot_Basic(t *testing.T) {
	snapshot, err := parseP2PDiscoverySnapshot([]byte(`{"sequence":12,"ttlSeconds":90,"peers":[{"agentId":"agent-a","peerId":"12D3KooWabc","addrs":["192.168.1.10"],"port":41080}]}`))
	if err != nil {
		t.Fatalf("parseP2PDiscoverySnapshot: %v", err)
	}
	if snapshot.Sequence != 12 {
		t.Fatalf("Sequence = %d", snapshot.Sequence)
	}
	if snapshot.TTLSeconds != 90 {
		t.Fatalf("TTLSeconds = %d", snapshot.TTLSeconds)
	}
	if len(snapshot.Peers) != 1 {
		t.Fatalf("Peers = %d", len(snapshot.Peers))
	}
	if snapshot.Peers[0].AgentID != "agent-a" {
		t.Fatalf("AgentID = %q", snapshot.Peers[0].AgentID)
	}
}

func TestResolveNATSSubjects_RejectsMissingClientOrSite(t *testing.T) {
	if _, err := resolveNATSSubjects(Config{SiteID: "site-1", AgentID: "agent-1"}); err == nil {
		t.Fatalf("expected error when clientId is missing")
	}
	if _, err := resolveNATSSubjects(Config{ClientID: "client-1", AgentID: "agent-1"}); err == nil {
		t.Fatalf("expected error when siteId is missing")
	}
}

func TestCanonicalSubjectSegment_RejectsInvalidCharacters(t *testing.T) {
	if _, err := canonicalSubjectSegment("clientId", "client.one"); err == nil {
		t.Fatalf("expected dot to be rejected")
	}
	if _, err := canonicalSubjectSegment("siteId", "site *"); err == nil {
		t.Fatalf("expected wildcard/space to be rejected")
	}
}

func TestValidateCanonicalNATSContext_RequiresAgentClientAndSite(t *testing.T) {
	if err := validateCanonicalNATSContext(Config{AgentID: "not-a-guid", ClientID: "client-1", SiteID: "site-1"}); err == nil {
		t.Fatalf("expected invalid agentId to be rejected")
	}
	if err := validateCanonicalNATSContext(Config{AgentID: "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7", SiteID: "site-1"}); err == nil {
		t.Fatalf("expected missing clientId to be rejected")
	}
	if err := validateCanonicalNATSContext(Config{AgentID: "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7", ClientID: "client-1"}); err == nil {
		t.Fatalf("expected missing siteId to be rejected")
	}
	if err := validateCanonicalNATSContext(Config{AgentID: "d2719a7d-43bb-4e7e-bbe6-18dce7bf1db7", ClientID: "client-1", SiteID: "site-1"}); err != nil {
		t.Fatalf("expected valid canonical context, got %v", err)
	}
}

func TestValidateAgentIdentityJWTClaims_ExactSubjects(t *testing.T) {
	subjects, err := resolveNATSSubjects(Config{ClientID: "client-1", SiteID: "site-1", AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	jwt := buildTestJWT(t, map[string]any{
		"nats": map[string]any{
			"sub": map[string]any{
				"allow": []string{
					subjects.CommandAgent,
					subjects.CommandSiteFanout,
					subjects.CommandClientFanout,
					subjects.CommandGlobalFanout,
					subjects.GlobalPong,
					subjects.SyncPing,
					subjects.P2PDiscovery,
				},
			},
			"pub": map[string]any{
				"allow": []string{
					subjects.Heartbeat,
					subjects.Result,
					subjects.Hardware,
					subjects.RemoteDebugLog,
				},
			},
		},
	})

	if err := validateAgentIdentityJWTClaims(jwt, subjects); err != nil {
		t.Fatalf("validateAgentIdentityJWTClaims: %v", err)
	}
}

func TestValidateAgentIdentityJWTClaims_RejectsExtraSubject(t *testing.T) {
	subjects, err := resolveNATSSubjects(Config{ClientID: "client-1", SiteID: "site-1", AgentID: "agent-1"})
	if err != nil {
		t.Fatalf("resolveNATSSubjects: %v", err)
	}

	jwt := buildTestJWT(t, map[string]any{
		"nats": map[string]any{
			"sub": map[string]any{
				"allow": []string{
					subjects.CommandAgent,
					subjects.CommandSiteFanout,
					subjects.CommandClientFanout,
					subjects.CommandGlobalFanout,
					subjects.GlobalPong,
					subjects.SyncPing,
					subjects.P2PDiscovery,
					"tenant.extra.agents.command",
				},
			},
			"pub": map[string]any{
				"allow": []string{
					subjects.Heartbeat,
					subjects.Result,
					subjects.Hardware,
					subjects.RemoteDebugLog,
				},
			},
		},
	})

	if err := validateAgentIdentityJWTClaims(jwt, subjects); err == nil {
		t.Fatalf("expected ACL validation to fail when JWT contains extra subscribe subject")
	}
}

func buildTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("json.Marshal claims: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".signature"
}
