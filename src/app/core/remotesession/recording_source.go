//go:build windows

package remotesession

import (
	"sync"
	"time"
)

// RecordingSource intercepta frames da sessão para gravação.
// Envia frames duplicados ao servidor via subject de recording.
type RecordingSource struct {
	mu             sync.Mutex
	enabled        bool
	startedAt      time.Time
	framesCaptured int64
	bytesCaptured  int64
	natsStream     *NatsStreamHandler
	sessionID      string
}

// NewRecordingSource cria um tap de gravação.
func NewRecordingSource(sessionID string, natsStream *NatsStreamHandler) *RecordingSource {
	return &RecordingSource{
		sessionID:  sessionID,
		natsStream: natsStream,
	}
}

// Start inicia a gravação.
func (rs *RecordingSource) Start() {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.enabled = true
	rs.startedAt = time.Now()
	rs.framesCaptured = 0
	rs.bytesCaptured = 0

	// Notifica o servidor que gravação iniciou
	rs.natsStream.PublishEvent(rs.sessionID, "recording_started", map[string]string{
		"startedAt": rs.startedAt.UTC().Format(time.RFC3339),
	})
}

// Stop interrompe a gravação.
func (rs *RecordingSource) Stop() {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if !rs.enabled {
		return
	}

	rs.enabled = false
	rs.natsStream.PublishEvent(rs.sessionID, "recording_stopped", map[string]any{
		"startedAt":      rs.startedAt.UTC().Format(time.RFC3339),
		"framesCaptured": rs.framesCaptured,
		"bytesCaptured":  rs.bytesCaptured,
		"durationSec":    time.Since(rs.startedAt).Seconds(),
	})
}

// IsEnabled retorna true se gravação está ativa.
func (rs *RecordingSource) IsEnabled() bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.enabled
}

// CaptureFrame envia um frame duplicado ao servidor para gravação.
// frameData já inclui o header binário.
func (rs *RecordingSource) CaptureFrame(frameData []byte) {
	rs.mu.Lock()
	if !rs.enabled {
		rs.mu.Unlock()
		return
	}
	rs.mu.Unlock()

	// Publica no subject de recording
	rs.natsStream.PublishFrame(rs.sessionID+".recording", frameData)

	rs.mu.Lock()
	rs.framesCaptured++
	rs.bytesCaptured += int64(len(frameData))
	rs.mu.Unlock()
}

// Stats retorna métricas da gravação.
func (rs *RecordingSource) Stats() (frames, bytes int64, durationSec float64) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.framesCaptured, rs.bytesCaptured, time.Since(rs.startedAt).Seconds()
}
