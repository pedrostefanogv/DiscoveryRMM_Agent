package remotesession

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
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
	codecSel   CodecSelector
	recording  *RecordingSource

	frameSeq  uint64
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// FrameHeader binario para frames (12 bytes).
//   seq (uint32) | ts (uint32 unix ms) | w (uint16) | h (uint16)
// O payload JPEG/WebP/H.264 segue apos o header.
const frameHeaderLen = 12

// NewSessionScreen cria uma nova sessao de screen capture.
func NewSessionScreen(sessionID string, natsStream *NatsStreamHandler) (*SessionScreen, error) {
	capturer, err := screen.NewCapturer(0) // monitor primario
	if err != nil {
		return nil, fmt.Errorf("screen capture: %w", err)
	}

	encoder := screen.NewJPEGEncoder()
	codecSel := NewCodecSelector()
	quality := NewQualityManager(QualityConfig{})
	recording := NewRecordingSource(sessionID, natsStream)

	return &SessionScreen{
		sessionID:  sessionID,
		capturer:   capturer,
		encoder:    encoder,
		natsStream: natsStream,
		quality:    &quality,
		codecSel:   codecSel,
		recording:  recording,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}, nil
}

// Start inicia o loop de captura e envio de frames.
// Bloqueia ate o contexto ser cancelado ou Stop() ser chamado.
func (s *SessionScreen) Start(ctx context.Context, fps int) error {
	defer close(s.doneCh)
	defer s.capturer.Close()

	_ = screen.DetectGPU() // GPU detection for future optimization (H.264 encoder selection)

	log.Printf("[remote-session-screen] Start: fps=%d capturer=%s\n", fps, s.capturer.Name())

	frameInterval := time.Second / time.Duration(fps)
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	quality := s.quality.Current()
	// Cache DXGI→GDI fallback state (melhoria M5)
	dxgiFailed := false

	var framesSent int64
	var framesSkipped int64
	lastLogTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[remote-session-screen] contexto cancelado apos %d frames enviados, %d pulados. Motivo: %v\n",
				framesSent, framesSkipped, ctx.Err())
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped", map[string]string{"reason": "context_cancelled"})
			return ctx.Err()

		case <-s.stopCh:
			log.Printf("[remote-session-screen] stop solicitado apos %d frames enviados, %d pulados\n",
				framesSent, framesSkipped)
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped", map[string]string{"reason": "stopped"})
			return nil

		case <-ticker.C:
			frame, err := s.capturer.AcquireNextFrame()
			if err != nil {
				// Fallback GDI se DXGI falhou — cache do estado (M5)
				if !dxgiFailed && s.capturer.Name() == "dxgi" {
					log.Printf("[remote-session-screen] DXGI falhou, tentando fallback GDI: %v\n", err)
					gdi, gdiErr := screen.NewGDICapturer()
					if gdiErr == nil {
						s.capturer.Close()
						s.capturer = gdi
						dxgiFailed = true
						log.Printf("[remote-session-screen] fallback GDI ativo\n")
					} else {
						log.Printf("[remote-session-screen] ERRO ao criar GDI capturer: %v\n", gdiErr)
					}
				}
				framesSkipped++
				continue
			}

			// Encode frame
			q := quality.JpegQuality
			encoded, err := s.encoder.Encode(frame, q)
			s.capturer.ReleaseFrame()
			if err != nil {
				log.Printf("[remote-session-screen] ERRO ao encodar frame: %v\n", err)
				framesSkipped++
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
				log.Printf("[remote-session-screen] ERRO ao publicar frame %d: %v\n", s.frameSeq, err)
				framesSkipped++
				continue
			}

			// Tap de gravação (G5)
			s.recording.CaptureFrame(buf)

			s.frameSeq++

			// Adaptacao de qualidade
			s.quality.RecordFrame(len(buf), time.Now())

			// Log periodico a cada 10s (aproximadamente 150 frames a 15fps)
			framesSent++
			if time.Since(lastLogTime) >= 10*time.Second {
				log.Printf("[remote-session-screen] status: %d frames enviados (%dx%d), %d pulados, ultimo frame %d bytes\n",
					framesSent, frame.Width, frame.Height, framesSkipped, len(buf))
				lastLogTime = time.Now()
			}
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

// SetCodec configura o codec preferencial (auto/jpeg/webp/h264).
func (s *SessionScreen) SetCodec(codec string) {
	s.codecSel.SetPreferred(codec)
	// Seleciona o encoder otimo com base na GPU e codec
	encoder := s.codecSel.Select(s.quality.Profile(), codec)
	if encoder != nil {
		s.encoder = encoder
	}
}
