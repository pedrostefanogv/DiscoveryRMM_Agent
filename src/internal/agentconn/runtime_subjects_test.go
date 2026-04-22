package agentconn

import "testing"

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
	if subjects.Command != prefix+".command" {
		t.Fatalf("Command = %q", subjects.Command)
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
	if subjects.SyncPing != prefix+".sync.ping" {
		t.Fatalf("SyncPing = %q", subjects.SyncPing)
	}
	if subjects.Dashboard != "tenant.client-1.site.site-1.dashboard.events" {
		t.Fatalf("Dashboard = %q", subjects.Dashboard)
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
