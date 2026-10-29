package inventory

import (
	"encoding/json"
	"testing"

	"discovery/app/core/thirdparty"
	"discovery/app/core/windowsupdate"
)

func TestThirdPartySignatureIsOrderIndependent(t *testing.T) {
	a := []thirdparty.AppUpdate{
		{Source: "choco", ID: "googlechrome", AvailableVersion: "121.0"},
		{Source: "winget", ID: "Google.Chrome", AvailableVersion: "121.0"},
	}
	b := []thirdparty.AppUpdate{a[1], a[0]}

	if thirdPartySignature(a) != thirdPartySignature(b) {
		t.Fatal("assinatura deve ignorar a ordem")
	}
}

func TestThirdPartySignatureChangesWithInventory(t *testing.T) {
	base := []thirdparty.AppUpdate{{Source: "choco", ID: "7zip", AvailableVersion: "24.0"}}
	baseHash := thirdPartySignature(base)

	bumped := []thirdparty.AppUpdate{{Source: "choco", ID: "7zip", AvailableVersion: "24.1"}}
	if thirdPartySignature(bumped) == baseHash {
		t.Fatal("nova versão deve mudar a assinatura")
	}

	added := append(append([]thirdparty.AppUpdate{}, base...),
		thirdparty.AppUpdate{Source: "winget", ID: "Google.Chrome", AvailableVersion: "121.0"})
	if thirdPartySignature(added) == baseHash {
		t.Fatal("app novo deve mudar a assinatura")
	}
}

func TestThirdPartySignatureFromEnvelope(t *testing.T) {
	envelope := agentWindowsUpdateEnvelope{
		AgentID:     "agent-1",
		CollectedAt: "2026-10-29T10:00:00Z",
		Scan:        agentWindowsUpdateScan{Status: "ok"},
		Updates: []windowsupdate.Update{
			{UpdateKey: "guid:11111111-1111-1111-1111-111111111111:1", Source: "wua", Title: "KB"},
			{UpdateKey: "choco:7zip", Source: "choco", Title: "7-Zip", AvailableVersion: "24.0"},
		},
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	fromEnvelope := thirdPartySignatureFromEnvelope(payload)
	expected := thirdPartySignature([]thirdparty.AppUpdate{
		{Source: "choco", ID: "7zip", AvailableVersion: "24.0"},
	})
	if fromEnvelope != expected {
		t.Fatalf("assinatura do envelope = %q; want %q", fromEnvelope, expected)
	}

	// Envelope sem apps de terceiros: assinatura do conjunto vazio (não vazia).
	onlyWua := agentWindowsUpdateEnvelope{
		AgentID:     "agent-1",
		CollectedAt: "2026-10-29T10:00:00Z",
		Scan:        agentWindowsUpdateScan{Status: "ok"},
		Updates:     []windowsupdate.Update{{UpdateKey: "kb:KB5034441", Source: "qfe"}},
	}
	wuaPayload, _ := json.Marshal(onlyWua)
	if thirdPartySignatureFromEnvelope(wuaPayload) != thirdPartySignature(nil) {
		t.Fatal("envelope sem terceiros deve usar a assinatura do conjunto vazio")
	}
}

func TestThirdPartySignatureFromEmptyPayload(t *testing.T) {
	if thirdPartySignatureFromEnvelope(nil) != "" {
		t.Fatal("payload vazio não tem assinatura")
	}
}
