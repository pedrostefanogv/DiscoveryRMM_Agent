package remotesession

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"discovery/internal/screen"
)

// SessionScreen gerencia uma sessao de screen capture remota.
type SessionScreen struct {
	sessionID  string
	capturer   screen.Capturer
	encoder    screen.Encoder
	natsStream *NatsStreamHandler
	quality    *QualityManager

	frameSeq  uint64
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// FrameHeader binario para frames (12 bytes).
//   seq (uint32) | ts (uint32 unix ms) | w (uint16) | h (uint16)
// O payload JPEG/WebP segue apos o header.
const frameHeaderLen = 12

// NewSessionScreen cria uma nova sessao de screen capture.
func NewSessionScreen(sessionID string, natsStream *NatsStreamHandler) (*SessionScreen, error) {
	capturer, err := screen.NewCapturer(0) // monitor primario
	if err != nil {
		return nil, fmt.Errorf("screen capture: %w", err)
	}

	encoder := screen.NewJPEGEncoder()
	quality := NewQualityManager(QualityConfig{})

	return &SessionScreen{
		sessionID:  sessionID,
		capturer:   capturer,
		encoder:    encoder,
		natsStream: natsStream,
		quality:    &quality,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}, nil
}

// Start inicia o loop de captura e envio de frames.
// Bloqueia ate o contexto ser cancelado ou Stop() ser chamado.
func (s *SessionScreen) Start(ctx context.Context, fps int) error {
	defer close(s.doneCh)
	defer s.capturer.Close()

	gpu := screen.DetectGPU()
	frameInterval := time.Second / time.Duration(fps)
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	quality := s.quality.Current()

	for {
		select {
		case <-ctx.Done():
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped", map[string]string{"reason": "context_cancelled"})
			return ctx.Err()

		case <-s.stopCh:
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped", map[string]string{"reason": "stopped"})
			return nil

		case <-ticker.C:
			frame, err := s.capturer.AcquireNextFrame()
			if err != nil {
				// Fallback GDI se DXGI falhou
				if s.capturer.Name() == "dxgi" {
					gdi, gdiErr := screen.NewGDICapturer()
					if gdiErr == nil {
						s.capturer.Close()
						s.capturer = gdi
					}
				}
				continue
			}

			// Encode JPEG
			q := quality.JpegQuality
			encoded, err := s.encoder.Encode(frame, q)
			s.capturer.ReleaseFrame()
			if err != nil {
				continue
			}

			// Monta frame com header binario
			ts := uint32(time.Now().UnixMilli())
			buf := make([]byte, frameHeaderLen+len(encoded))
			binary.BigEndian.PutUint32(buf[0:4], uint32(s.frameSeq))
			binary.BigEndian.PutUint32(buf[4:8], ts)
			binary.BigEndian.PutUint16(buf[8:10], uint16(frame.Width))
			binary.BigEndian.PutUint16(buf[10:12], uint16(frame.Height))
			copy(buf[frameHeaderLen:], encoded)

			if err := s.natsStream.PublishFrame(s.sessionID, buf); err != nil {
				continue
			}

			s.frameSeq++

			// Adaptacao de qualidade
			s.quality.RecordFrame(len(buf), time.Now())

			_ = gpu // gpu info available for future optimization
			_ = json.Marshal // keep json import
		}
	}
}

// Stop encerra o loop de captura.
func (s *SessionScreen) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	<-s.doneCh // aguarda loop terminar
}

// SetQuality atualiza o perfil de qualidade.
func (s *SessionScreen) SetQuality(profile string) {
	s.quality.SetProfile(profile)
}
