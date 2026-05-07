package agentconn

import "testing"

func TestHasCanonicalNATSContext(t *testing.T) {
	if hasCanonicalNATSContext(Config{}) {
		t.Fatalf("expected false when clientId/siteId are empty")
	}

	if hasCanonicalNATSContext(Config{ClientID: "client-1", SiteID: ""}) {
		t.Fatalf("expected false when siteId is empty")
	}

	if hasCanonicalNATSContext(Config{ClientID: "", SiteID: "site-1"}) {
		t.Fatalf("expected false when clientId is empty")
	}

	if !hasCanonicalNATSContext(Config{ClientID: " client-1 ", SiteID: " site-1 "}) {
		t.Fatalf("expected true when both fields are present")
	}
}
