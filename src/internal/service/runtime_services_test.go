package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"discovery/internal/database"
	"discovery/internal/models"
)

func TestInventoryRuntimeServiceCollect_RequiresProvisioning(t *testing.T) {
	svc := &inventoryRuntimeService{
		loadConfig: func() *SharedConfig {
			return &SharedConfig{}
		},
	}

	_, err := svc.Collect(context.Background())
	if err == nil {
		t.Fatalf("expected provisioning error")
	}
	if !strings.Contains(err.Error(), "nao estiver provisionado") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInventoryRuntimeServiceCollect_SyncsHardwareAndSoftwareWhenProvisioned(t *testing.T) {
	const agentID = "8f6d6d72-4a8a-4c87-bffa-34ba29dc0bb7"

	hardwareCalls := 0
	softwareCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Agent-ID"); got != agentID {
			t.Fatalf("X-Agent-ID = %q", got)
		}

		switch r.URL.Path {
		case "/api/v1/agent-auth/me/hardware":
			hardwareCalls++
		case "/api/v1/agent-auth/me/software":
			softwareCalls++
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer db.Close()

	svc := &inventoryRuntimeService{
		loadConfig: func() *SharedConfig {
			return &SharedConfig{
				AgentID:   agentID,
				ApiScheme: parsed.Scheme,
				ApiServer: parsed.Host,
				AuthToken: "token-123",
			}
		},
		collect: func(context.Context) (models.InventoryReport, error) {
			return models.InventoryReport{
				CollectedAt: "2026-05-03T02:40:00Z",
				Hardware: models.HardwareInfo{
					Hostname: "pc-teste",
				},
				OS: models.OperatingSystem{
					Name:    "Windows 11 Pro",
					Version: "10.0.26100",
				},
				Software: []models.SoftwareItem{{
					Name:    "Discovery Agent",
					Version: "1.0.0",
				}},
			}, nil
		},
		db:      db,
		logf:    func(string) {},
		version: "dev",
	}

	_, err = svc.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect returned error: %v", err)
	}
	if hardwareCalls != 1 {
		t.Fatalf("hardwareCalls = %d", hardwareCalls)
	}
	if softwareCalls != 1 {
		t.Fatalf("softwareCalls = %d", softwareCalls)
	}
	if svc.db == nil {
		t.Fatal("expected db to remain attached for snapshot persistence")
	}
}
