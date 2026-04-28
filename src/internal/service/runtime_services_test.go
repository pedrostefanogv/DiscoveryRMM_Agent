package service

import (
	"context"
	"strings"
	"testing"
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
