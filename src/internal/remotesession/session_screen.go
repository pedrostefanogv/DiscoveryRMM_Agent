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
// Pipeline otimizado com:
//   - Dirty rect detection (software tile diff)
//   - Frame skip em idle (0 bytes quando tela parada — maior economia)
//   - Bounded channel backpressure (capacity=2, igual ControlR)
//   - Adaptive throttle (0ms com pouca mudanca, delay proporcional com muita)
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

	// Dirty rect detection
	dirtyDetector *screen.DirtyDetector
	useDirtyRect  bool // ativado apos primeiro frame completo

	frameSeq uint64
	stopCh   chan struct{}
	doneCh   chan struct{}

	// Estatisticas para log/metrics
	lastLogTime     time.Time
	lastMetricsTime time.Time
}

// FrameHeader binario para frames (12 bytes).
//
//	seq (uint32) | ts (uint32 unix ms) | w (uint16) | h (uint16)
//
// O payload JPEG/WebP/H.264 segue apos o header.
const frameHeaderLen = 12

// Constantes de timing
const (
	idleDelay          = 50 * time.Millisecond // delay quando tela esta parada
	afterFailureDelay  = 100 * time.Millisecond // delay apos erro de captura
	dirtyRectCheckFreq = 60                     // frames entre verificacao completa (a cada ~2-3s em 30fps)
	deepIdleThreshold  = 15                     // frames idle consecutivos antes de deep idle (~3s com DXGI timeout 200ms)
)

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
		sessionID:       sessionID,
		capturer:        capturer,
		encoder:         encoder,
		natsStream:      natsStream,
		quality:         &quality,
		codecSel:        codecSel,
		recording:       recording,
		inputCtrl:       inputCtrl,
		dirtyDetector:   screen.NewDirtyDetector(32),
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
		lastLogTime:     time.Now(),
		lastMetricsTime: time.Now(),
	}, nil
}

// Start inicia o loop de captura e envio de frames com pipeline otimizado.
// Pipeline: captura → detect dirty rects → skip idle / async encode → publish
// Encode roda em goroutine separada (overlap com captura do proximo frame).
func (s *SessionScreen) Start(ctx context.Context, fps int) error {
	defer close(s.doneCh)
	defer s.capturer.Close()

	_ = screen.DetectGPU()

	if fps <= 0 {
		fps = s.quality.Current().Fps
	}
	if fps <= 0 {
		fps = 15
	}

	log.Printf("[remote-session-screen] Start: fps=%d capturer=%s dirtyRects=true asyncEncode=true\n",
		fps, s.capturer.Name())

	// ── Encode worker: goroutine dedicada para JPEG (overlap com captura) ──
	type encodeJob struct {
		frame  *screen.Frame
		width  int
		height int
		seq    uint64
	}

	type frameResult struct {
		data   []byte
		seq    uint64
		width  int
		height int
		rawBytes int64
	}

	encodeChan := make(chan encodeJob, 1) // buffer=1: captura nunca bloqueia se encoder ocupado
	resultChan := make(chan frameResult, 2)

	var encodeWg sync.WaitGroup
	encodeWg.Add(1)
	go func() {
		defer encodeWg.Done()
		for job := range encodeChan {
			enc := s.getEncoder()
			encoded, err := enc.Encode(job.frame, s.quality.Current().EffectiveJpegQuality())
			if err != nil {
				log.Printf("[remote-session-screen] ERRO encode: %v\n", err)
				continue
			}
			// Monta header binario (12 bytes) + payload JPEG
			buf := make([]byte, frameHeaderLen+len(encoded))
			binary.BigEndian.PutUint32(buf[0:4], uint32(job.seq))
			binary.BigEndian.PutUint32(buf[4:8], uint32(time.Now().UnixMilli()))
			binary.BigEndian.PutUint16(buf[8:10], uint16(job.width))
			binary.BigEndian.PutUint16(buf[10:12], uint16(job.height))
			copy(buf[frameHeaderLen:], encoded)

			select {
			case resultChan <- frameResult{
				data:   buf,
				seq:    job.seq,
				width:  job.width,
				height: job.height,
				rawBytes: int64(len(job.frame.Data)),
			}:
			case <-ctx.Done():
				return
			case <-s.stopCh:
				return
			}
		}
	}()

	// ── Consumer: publica resultados do encode ──
	type frameJob struct{ data []byte }
	frameChan := make(chan frameJob, 2)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for job := range frameChan {
			if err := s.natsStream.PublishFrame(s.sessionID, job.data); err != nil {
				log.Printf("[remote-session-screen] ERRO ao publicar frame: %v\n", err)
			}
		}
	}()

	// ── Producer loop (captura rapida, sem bloqueio de encode) ──
	var totalSent, totalSkipped, totalIdle int64
	var sent, skipped int64
	var rawBytes, encBytes int64
	var encodeTimeTotal time.Duration
	consecutiveIdle := 0
	noChangeCount := 0
	curFps := fps

	for {
		select {
		case <-ctx.Done():
			log.Printf("[remote-session-screen] ctx cancelado: %d env, %d skip, %d idle\n",
				totalSent, totalSkipped, totalIdle)
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped",
				map[string]string{"reason": "context_cancelled"})
			close(encodeChan)
			encodeWg.Wait()
			close(resultChan)
			close(frameChan)
			wg.Wait()
			return ctx.Err()

		case <-s.stopCh:
			log.Printf("[remote-session-screen] stop: %d env, %d skip, %d idle\n",
				totalSent, totalSkipped, totalIdle)
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped",
				map[string]string{"reason": "stopped"})
			close(encodeChan)
			encodeWg.Wait()
			close(resultChan)
			close(frameChan)
			wg.Wait()
			return nil

		// Resultado do encode worker — publica e atualiza metricas
		case result := <-resultChan:
			select {
			case frameChan <- frameJob{data: result.data}:
			case <-ctx.Done():
				return ctx.Err()
			case <-s.stopCh:
				return nil
			}

			s.inputCtrl.UpdateFrameMetrics(result.width, result.height, result.width, result.height)
			s.recording.CaptureFrame(result.data)
			s.frameSeq++
			s.quality.RecordFrame(len(result.data), time.Now())

			rawBytes += result.rawBytes
			encBytes += int64(len(result.data) - frameHeaderLen)
			sent++
			totalSent++

			// Metricas a cada 5s
			if time.Since(s.lastMetricsTime) >= 5*time.Second {
				s.publishMetrics(result.width, result.height, sent, skipped, rawBytes, encBytes, encodeTimeTotal)
				s.lastMetricsTime = time.Now()
			}

			// Log a cada 10s
			if time.Since(s.lastLogTime) >= 10*time.Second {
				compRatio := float64(0)
				avgEncMs := float64(0)
				if rawBytes > 0 {
					compRatio = float64(rawBytes) / float64(encBytes)
				}
				if sent > 0 {
					avgEncMs = float64(encodeTimeTotal.Milliseconds()) / float64(sent)
				}
				log.Printf("[remote-session-screen] %d env, %d idle | comp %.1f:1 | enc %.1fms | fps=%d\n",
					sent, totalIdle, compRatio, avgEncMs, curFps)
				s.lastLogTime = time.Now()
				sent, skipped = 0, 0
				rawBytes, encBytes = 0, 0
				encodeTimeTotal = 0
			}
			continue

		default:
		}

		q := s.quality.Current()
		curFps = q.EffectiveFps()
		if curFps <= 0 {
			curFps = 15
		}

		// Backpressure: nao captura se encoder estiver ocupado (buffer=1 cheio)
		if len(encodeChan) >= 1 {
			time.Sleep(time.Millisecond)
			continue
		}

		// Throttle adaptativo baseado em curFps
		targetInterval := time.Second / time.Duration(curFps)

		// Capture
		frame, err := s.capturer.AcquireNextFrame()
		if err != nil {
			skipped++
			totalSkipped++
			time.Sleep(idleDelay)
			continue
		}

		// Aplica escala
		scaleFactor := q.ScaleFactor
		ownsFrame := false
		if scaleFactor > 0 && scaleFactor < 1.0 {
			resized := screen.ResizeBGRA(frame, scaleFactor)
			s.capturer.ReleaseFrame()
			frame = resized
			ownsFrame = true
		}

		// Dirty rect detection — pula frames idle
		noChangeCount++
		rects := s.dirtyDetector.Detect(frame)

		if len(rects) == 0 && s.useDirtyRect && noChangeCount < dirtyRectCheckFreq {
			if !ownsFrame {
				s.capturer.ReleaseFrame()
			}
			totalIdle++
			consecutiveIdle++
			if consecutiveIdle > deepIdleThreshold {
				time.Sleep(idleDelay * 2)
			}
			continue
		}
		consecutiveIdle = 0
		noChangeCount = 0

		// Copia frame do GPU
		if !ownsFrame {
			frameCopy := &screen.Frame{
				Data:   make([]byte, len(frame.Data)),
				Width:  frame.Width,
				Height: frame.Height,
				Stride: frame.Stride,
			}
			copy(frameCopy.Data, frame.Data)
			s.capturer.ReleaseFrame()
			frame = frameCopy
		}
		s.useDirtyRect = true

		// Envia para encode worker (nao bloqueante com buffer=1)
		select {
		case encodeChan <- encodeJob{
			frame:  frame,
			width:  frame.Width,
			height: frame.Height,
			seq:    s.frameSeq,
		}:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stopCh:
			return nil
		}

		// Throttle: espera o tempo restante do intervalo FPS
		// O encode roda em paralelo — nao impacta a latencia de captura
		time.Sleep(targetInterval / 3)
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

// getEncoder retorna o encoder atual thread-safe.
func (s *SessionScreen) getEncoder() screen.Encoder {
	s.encoderMu.RLock()
	defer s.encoderMu.RUnlock()
	return s.encoder
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
