package remotesession

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"time"

	"discovery/internal/screen"
)

// SessionScreen gerencia uma sessao de screen capture remota.
type SessionScreen struct {
	sessionID  string
	capturer   screen.Capturer
	encoder    screen.Encoder
	encoderMu  sync.RWMutex // protege troca de encoder (SetCodec)
	natsStream *NatsStreamHandler
	quality    *QualityManager
	codecSel   CodecSelector
	recording  *RecordingSource
	inputCtrl  *InputController

	frameSeq uint64
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// FrameHeader binario para frames (12 bytes).
//
//	seq (uint32) | ts (uint32 unix ms) | w (uint16) | h (uint16)
//
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
	inputCtrl := NewInputController(sessionID)

	return &SessionScreen{
		sessionID:  sessionID,
		capturer:   capturer,
		encoder:    encoder,
		natsStream: natsStream,
		quality:    &quality,
		codecSel:   codecSel,
		recording:  recording,
		inputCtrl:  inputCtrl,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}, nil
}

// Start inicia o loop de captura e envio de frames.
// Bloqueia ate o contexto ser cancelado ou Stop() ser chamado.
// fps: se <= 0, usa o FPS do perfil de qualidade atual.
func (s *SessionScreen) Start(ctx context.Context, fps int) error {
	defer close(s.doneCh)
	defer s.capturer.Close()

	_ = screen.DetectGPU() // GPU detection for future optimization (H.264 encoder selection)

	if fps <= 0 {
		fps = s.quality.Current().Fps
	}
	if fps <= 0 {
		fps = 15
	}

	log.Printf("[remote-session-screen] Start: fps=%d capturer=%s\n", fps, s.capturer.Name())

	frameInterval := time.Second / time.Duration(fps)
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()

	// quality é lido dinamicamente a cada frame para suportar mudancas em tempo real.
	// A variavel local captura o snapshot inicial para o ticker.

	var totalFramesSent int64
	var totalFramesSkipped int64
	var framesSent int64
	var framesSkipped int64
	var rawBytesTotal int64
	var encBytesTotal int64
	var encodeTimeTotal time.Duration
	lastLogTime := time.Now()
	lastMetricsTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[remote-session-screen] contexto cancelado apos %d frames enviados, %d pulados. Motivo: %v\n",
				totalFramesSent, totalFramesSkipped, ctx.Err())
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped", map[string]string{"reason": "context_cancelled"})
			return ctx.Err()

		case <-s.stopCh:
			log.Printf("[remote-session-screen] stop solicitado apos %d frames enviados, %d pulados\n",
				totalFramesSent, totalFramesSkipped)
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped", map[string]string{"reason": "stopped"})
			return nil

		case <-ticker.C:
			// Le qualidade atual a cada frame — permite SetQuality/SetImageQuality/SetMaxFps runtime
			q := s.quality.Current()
			curFps := q.EffectiveFps()
			if curFps <= 0 {
				curFps = 15
			}
			// Ajusta ticker se FPS mudou
			newInterval := time.Second / time.Duration(curFps)
			if newInterval != frameInterval {
				frameInterval = newInterval
				ticker.Reset(frameInterval)
			}

			captureStart := time.Now()
			frame, err := s.capturer.AcquireNextFrame()
			captureMs := float64(time.Since(captureStart).Microseconds()) / 1000.0
			if err != nil {
				log.Printf("[remote-session-screen] ERRO ao capturar frame: %v (captureMs=%.1f)\n", err, captureMs)
				framesSkipped++
				totalFramesSkipped++
				continue
			}

			// Aplica escala — redimensiona antes do encode se ScaleFactor < 1.0
			scaleFactor := q.ScaleFactor
			scaled := false
			if scaleFactor > 0 && scaleFactor < 1.0 {
				scaled = true
				resized := screen.ResizeBGRA(frame, scaleFactor) // lê do frame original ANTES de liberar
				s.capturer.ReleaseFrame()                          // libera GPU resource do original
				frame = resized                                    // substitui pelo buffer alocado
			}
			_ = captureMs // reservado para telemetria futura

			// Encode frame (protege leitura do encoder com RLock)
			jpgQuality := q.EffectiveJpegQuality()
			rawSize := len(frame.Data)
			encodeStart := time.Now()
			s.encoderMu.RLock()
			enc := s.encoder
			s.encoderMu.RUnlock()
			encoded, err := enc.Encode(frame, jpgQuality)
			if !scaled {
				s.capturer.ReleaseFrame()
			}
			// se scaled=true, o frame é um buffer Go alocado — GC cuida
			if err != nil {
				log.Printf("[remote-session-screen] ERRO ao encodar frame: %v\n", err)
				framesSkipped++
				totalFramesSkipped++
				continue
			}
			encSize := len(encoded)
			rawBytesTotal += int64(rawSize)
			encBytesTotal += int64(encSize)
			encodeTimeTotal += time.Since(encodeStart)

			// Monta frame com header binario (usa dimensões efetivas após escala)
			ts := uint32(time.Now().UnixMilli())
			buf := make([]byte, frameHeaderLen+encSize)
			binary.BigEndian.PutUint32(buf[0:4], uint32(s.frameSeq))
			binary.BigEndian.PutUint32(buf[4:8], ts)
			binary.BigEndian.PutUint16(buf[8:10], uint16(frame.Width))
			binary.BigEndian.PutUint16(buf[10:12], uint16(frame.Height))
			copy(buf[frameHeaderLen:], encoded)

			publishStart := time.Now()
			if err := s.natsStream.PublishFrame(s.sessionID, buf); err != nil {
				log.Printf("[remote-session-screen] ERRO ao publicar frame %d: %v\n", s.frameSeq, err)
				framesSkipped++
				totalFramesSkipped++
				continue
			}
			_ = publishStart // reservado para telemetria futura

			// Atualiza métricas de input para coordenadas
			s.inputCtrl.UpdateFrameMetrics(frame.Width, frame.Height, frame.Width, frame.Height)

			// Tap de gravação (G5)
			s.recording.CaptureFrame(buf)

			s.frameSeq++

			// Adaptacao de qualidade
			s.quality.RecordFrame(len(buf), time.Now())

			// Publica metricas a cada 5s para o viewer (P2)
			framesSent++
			totalFramesSent++
			if time.Since(lastMetricsTime) >= 5*time.Second {
				s.publishMetrics(int(frame.Width), int(frame.Height), framesSent, framesSkipped, rawBytesTotal, encBytesTotal, encodeTimeTotal)
				lastMetricsTime = time.Now()
			}

			// Log periodico a cada 10s com metricas de compressao
			if time.Since(lastLogTime) >= 10*time.Second {
				compressionRatio := float64(0)
				avgEncodeMs := float64(0)
				if rawBytesTotal > 0 {
					compressionRatio = float64(rawBytesTotal) / float64(encBytesTotal)
				}
				if framesSent > 0 {
					avgEncodeMs = float64(encodeTimeTotal.Milliseconds()) / float64(framesSent)
				}
				log.Printf("[remote-session-screen] status: %d frames enviados (%dx%d), %d pulados, ultimo frame %d bytes | compressao %.1f:1 (raw=%dKB enc=%dKB) | encode avg %.1fms\n",
					framesSent, frame.Width, frame.Height, framesSkipped, len(buf),
					compressionRatio, rawBytesTotal/1024, encBytesTotal/1024, avgEncodeMs)
				lastLogTime = time.Now()
				framesSent = 0
				framesSkipped = 0
				rawBytesTotal = 0
				encBytesTotal = 0
				encodeTimeTotal = 0
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
		s.encoderMu.Lock()
		s.encoder = encoder
		s.encoderMu.Unlock()
	}
}

// SetImageQuality define a compressão da imagem (1-100). Sobrescreve o perfil.
func (s *SessionScreen) SetImageQuality(q int) {
	if q >= 1 && q <= 100 {
		s.quality.SetImageQuality(q)
	}
}

// ClearImageQuality remove o override de imagem (volta ao perfil).
func (s *SessionScreen) ClearImageQuality() {
	s.quality.ClearImageQuality()
}

// SetMaxFps define a taxa máxima de quadros por segundo. Sobrescreve o perfil.
func (s *SessionScreen) SetMaxFps(fps int) {
	if fps >= 1 && fps <= 60 {
		s.quality.SetMaxFps(fps)
	}
}

// ClearMaxFps remove o override de FPS (volta ao perfil).
func (s *SessionScreen) ClearMaxFps() {
	s.quality.ClearMaxFps()
}

// publishMetrics publica metricas de streaming no subject .event para o viewer.
func (s *SessionScreen) publishMetrics(width, height int, framesSent, framesSkipped int64, rawBytes, encBytes int64, encodeTimeTotal time.Duration) {
	q := s.quality.Current()
	compressionRatio := float64(0)
	avgEncodeMs := float64(0)
	if rawBytes > 0 {
		compressionRatio = float64(rawBytes) / float64(encBytes)
	}
	if framesSent > 0 {
		avgEncodeMs = float64(encodeTimeTotal.Milliseconds()) / float64(framesSent)
	}

	metrics := map[string]any{
		"eventType":       "metrics",
		"effectiveFps":    q.EffectiveFps(),
		"profileFps":      q.Fps,
		"quality":         s.quality.Profile(),
		"imageQuality":    q.EffectiveJpegQuality(),
		"resolution":      fmt.Sprintf("%dx%d", width, height),
		"compressionRatio": compressionRatio,
		"avgEncodeMs":     avgEncodeMs,
		"frameBytesAvg":   int64(0),
		"framesSent5s":    framesSent,
		"framesSkipped5s": framesSkipped,
		"totalFrames":     s.frameSeq,
	}
	if framesSent > 0 {
		metrics["frameBytesAvg"] = encBytes / framesSent
	}

	s.natsStream.PublishEvent(s.sessionID, "metrics", metrics)
}
