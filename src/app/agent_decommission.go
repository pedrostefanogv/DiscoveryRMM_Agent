package app

import (
	"context"
	"strings"
	"time"

	"discovery/app/core/database"
	"discovery/app/decommission"
)

// decommissionSvc é o service de decommission do agente.
var decommissionSvc *decommission.Service

// RunAgentDecommissionCleanup executa o DELETE do agente no backend.
// Em falha transitória, persiste um outbox local para retry no próximo startup.
func RunAgentDecommissionCleanup(ctx context.Context) error {
	if decommissionSvc == nil {
		return nil
	}
	return decommissionSvc.RunCleanup(ctx)
}

func runAgentDecommissionRemoteCleanup(ctx context.Context) error {
	if decommissionSvc == nil {
		return nil
	}
	return decommissionSvc.RunRemoteCleanup(ctx)
}

func cleanupAgentDecommissionLocalTempDirs() error {
	if decommissionSvc == nil {
		return nil
	}
	return decommissionSvc.CleanupLocalTempDirs()
}

func cleanupAgentDecommissionPaths(paths []string) error {
	return decommission.CleanupPaths(paths)
}

func (a *App) drainAgentDecommissionOutbox(ctx context.Context, reason string) {
	if a == nil || a.db == nil {
		return
	}
	sent, err := decommission.DrainOutbox(a.db, ctx)
	if err != nil {
		a.logs.append("[agent][decommission] erro ao drenar outbox (" + strings.TrimSpace(reason) + "): " + err.Error())
		return
	}
	if sent {
		a.logs.append("[agent][decommission] outbox de delete processado com sucesso")
	}
}

func resolveAgentDecommissionTargetFromInstaller() (decommission.Target, error) {
	if decommissionSvc == nil {
		return decommission.Target{}, nil
	}
	return decommissionSvc.ResolveTargetFromInstaller()
}

func parseInstallerServerURLLite(raw string) (string, string) {
	return decommission.ParseInstallerServerURLLite(raw)
}

func performAgentDecommissionDelete(ctx context.Context, target decommission.Target) error {
	return decommission.PerformDelete(ctx, target)
}

func enqueueAgentDecommissionOutbox(db *database.DB, target decommission.Target, cause error) error {
	return decommission.EnqueueOutbox(db, target, cause)
}

func drainAgentDecommissionOutbox(db *database.DB, ctx context.Context) (bool, error) {
	return decommission.DrainOutbox(db, ctx)
}

func parseRFC3339(value string) time.Time {
	return decommission.ParseRFC3339(value)
}

func agentDecommissionBackoff(attempt int) time.Duration {
	return decommission.Backoff(attempt)
}
