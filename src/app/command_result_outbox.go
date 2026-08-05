package app

import (
	"time"

	"discovery/app/core/agentconn"
)

// Bridges de outbox de resultados de comando. A lógica foi movida para o
// pacote sync (sync.Service → sync.CommandOutbox); estes métodos delegam para
// a instância do *App e são usados como callbacks do agentconn.
func (a *App) enqueueCommandResultOutbox(transport, dispatchID, commandID string, exitCode int, output, errText, sendError string) error {
	if a.syncSvc == nil {
		return nil
	}
	return a.syncSvc.EnqueueCommandResultOutbox(transport, dispatchID, commandID, exitCode, output, errText, sendError)
}

func (a *App) listDueCommandResultOutbox(transport string, now time.Time, limit int) ([]agentconn.CommandResultOutboxItem, error) {
	if a.syncSvc == nil {
		return nil, nil
	}
	return a.syncSvc.ListDueCommandResultOutbox(transport, now, limit)
}

func (a *App) markSentCommandResultOutbox(id int64) error {
	if a.syncSvc == nil {
		return nil
	}
	return a.syncSvc.MarkSentCommandResultOutbox(id)
}

func (a *App) rescheduleCommandResultOutbox(id int64, attempts int, nextAttemptAt time.Time, lastError string) error {
	if a.syncSvc == nil {
		return nil
	}
	return a.syncSvc.RescheduleCommandResultOutbox(id, attempts, nextAttemptAt, lastError)
}
