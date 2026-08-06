//go:build windows

package remotesession

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"time"

	"discovery/app/core/screen"
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

	tileMode bool // quando true, envia apenas os tiles alterados (EncodeDirtyRects)

	// Cursor separado: frame não contém cursor; posição/estado via subject .cursor
	cursorSeparate bool
	cursorSender   *screen.CursorSpriteSender
	cursorStopCh   chan struct{}

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
	idleDelay         = 50 * time.Millisecond  // delay quando tela esta parada
	afterFailureDelay = 100 * time.Millisecond // delay apos erro de captura
	deepIdleThreshold = 15                     // frames idle consecutivos antes de deep idle (~3s com DXGI timeout 200ms)
)

// NewSessionScreen cria uma nova sessao de screen capture.
// monitorIndex permite selecionar um monitor (0 = primário); use -1 para o padrão.
func NewSessionScreen(sessionID string, natsStream *NatsStreamHandler) (*SessionScreen, error) {
	return NewSessionScreenMonitor(sessionID, natsStream, 0)
}

// NewSessionScreenMonitor cria uma sessao de screen capture de um monitor específico.
func NewSessionScreenMonitor(sessionID string, natsStream *NatsStreamHandler, monitorIndex int) (*SessionScreen, error) {
	// Cursor separado por padrão (P2): frame sem cursor, cursor via subject .cursor.
	capturer, err := screen.NewCapturerMode(monitorIndex, false)
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
		cursorSender:    screen.NewCursorSpriteSender(),
		cursorStopCh:    make(chan struct{}),
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

	// fps: >0 = máximo; 0 = sem limite. Se ausente, usa o perfil.
	if fps <= 0 {
		fps = s.quality.Current().EffectiveFps()
	}

	log.Printf("[remote-session-screen] Start: fps=%d capturer=%s\n", fps, s.capturer.Name())

	// ── Cursor separado (P2): publica posição/estado do cursor quando muda ──
	if s.cursorSeparate && s.cursorSender != nil {
		go func() {
			ticker := time.NewTicker(16 * time.Millisecond) // ~60Hz
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					info, err := screen.GetCursorPos()
					if err != nil {
						continue
					}
					if s.cursorSender.ShouldSend(info) {
						_ = s.natsStream.PublishCursor(s.sessionID, s.cursorSender.Encode(info))
					}
					// Envia o bitmap do cursor apenas quando a FORMA muda
					// (handle diferente) — evita re-encodar PNG a cada frame.
					handle := screen.GetCursorHandle()
					if s.cursorSender.ShouldSendImage(handle) {
						if img := screen.CaptureCursorImage(); img != nil {
							_ = s.natsStream.PublishCursorImage(s.sessionID, img)
						}
					}
				case <-ctx.Done():
					return
				case <-s.cursorStopCh:
					return
				}
			}
		}()
	}

	// ── Encode worker: goroutine dedicada para JPEG (overlap com captura) ──
	type encodeJob struct {
		frame  *screen.Frame
		width  int
		height int
		seq    uint64
		rects  []screen.DirtyRect // dirty rects (modo tile); nil = frame completo
	}

	type frameResult struct {
		data     []byte
		seq      uint64
		width    int
		height   int
		rawBytes int64
		encodeMs float64
	}

	encodeChan := make(chan encodeJob, 1) // buffer=1: captura nunca bloqueia se encoder ocupado
	resultChan := make(chan frameResult, 2)

	var encodeWg sync.WaitGroup
	encodeWg.Add(1)
	go func() {
		defer encodeWg.Done()
		for job := range encodeChan {
			encStart := time.Now()
			enc := s.getEncoder()
			quality := s.quality.Current().EffectiveJpegQuality()
			var encoded []byte
			if job.rects != nil {
				// Modo tile: codifica apenas os rects alterados (economia de banda)
				tiles, err := screen.EncodeDirtyRects(job.frame, job.rects, quality)
				if err != nil {
					log.Printf("[remote-session-screen] ERRO encode tiles: %v\n", err)
					continue
				}
				encoded = tiles
			} else {
				var err error
				encoded, err = enc.Encode(job.frame, quality)
				if err != nil {
					log.Printf("[remote-session-screen] ERRO encode: %v\n", err)
					continue
				}
			}
			encMs := float64(time.Since(encStart).Microseconds()) / 1000.0
			// Monta header binario (12 bytes) + payload (JPEG frame ou tiles)
			buf := make([]byte, frameHeaderLen+len(encoded))
			binary.BigEndian.PutUint32(buf[0:4], uint32(job.seq))
			binary.BigEndian.PutUint32(buf[4:8], uint32(time.Now().UnixMilli()))
			binary.BigEndian.PutUint16(buf[8:10], uint16(job.width))
			binary.BigEndian.PutUint16(buf[10:12], uint16(job.height))
			copy(buf[frameHeaderLen:], encoded)

			select {
			case resultChan <- frameResult{
				data:     buf,
				seq:      job.seq,
				width:    job.width,
				height:   job.height,
				rawBytes: int64(len(job.frame.Data)),
				encodeMs: encMs,
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
	var captureMsTotal, dirtyMsTotal, copyMsTotal float64
	consecutiveIdle := 0
	curFps := fps
	loopStart := time.Now()

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
			encodeTimeTotal += time.Duration(result.encodeMs * float64(time.Millisecond))
			sent++
			totalSent++

			// Metricas a cada 5s
			if time.Since(s.lastMetricsTime) >= 5*time.Second {
				s.publishMetrics(result.width, result.height, sent, skipped, rawBytes, encBytes, encodeTimeTotal)
				s.lastMetricsTime = time.Now()
			}

			// Log detalhado a cada 10s — timing de cada etapa do pipeline
			if time.Since(s.lastLogTime) >= 10*time.Second {
				compRatio := float64(0)
				avgEncMs := float64(0)
				avgCapMs := float64(0)
				avgDirtyMs := float64(0)
				avgCopyMs := float64(0)
				if rawBytes > 0 {
					compRatio = float64(rawBytes) / float64(encBytes)
				}
				if sent > 0 {
					avgEncMs = float64(encodeTimeTotal.Milliseconds()) / float64(sent)
					avgCapMs = captureMsTotal / float64(sent)
					avgDirtyMs = dirtyMsTotal / float64(sent)
					avgCopyMs = copyMsTotal / float64(sent)
				}
				log.Printf("[remote-session-screen] %d env, %d idle | comp %.1f:1 | cap=%.1fms dirty=%.1fms copy=%.1fms enc=%.1fms | fps=%d\n",
					sent, totalIdle, compRatio, avgCapMs, avgDirtyMs, avgCopyMs, avgEncMs, curFps)
				s.lastLogTime = time.Now()
				sent, skipped = 0, 0
				rawBytes, encBytes = 0, 0
				encodeTimeTotal = 0
				captureMsTotal, dirtyMsTotal, copyMsTotal = 0, 0, 0
			}
			continue

		default:
		}

		q := s.quality.Current()
		curFps = q.EffectiveFps()

		// Backpressure: nao captura se encoder estiver ocupado (buffer=1 cheio)
		if len(encodeChan) >= 1 {
			time.Sleep(time.Millisecond)
			continue
		}

		// ── Capture (timing) ──
		capStart := time.Now()
		frame, err := s.capturer.AcquireNextFrame()
		capMs := float64(time.Since(capStart).Microseconds()) / 1000.0
		if err != nil {
			skipped++
			totalSkipped++
			time.Sleep(idleDelay)
			continue
		}
		captureMsTotal += capMs

		// ownsFrame indica se o frame atual é um buffer alocado por nós
		// (tone mapping/escala) e não pertence mais ao capturer.
		ownsFrame := false

		// ── Tone mapping HDR→SDR (ANTES da escala) ──
		// Quando o monitor é HDR, o capturador DXGI manual entrega o frame em
		// scRGB (R16G16B16A16_FLOAT, ColorSpace != 0). Converte para SDR 8-bit
		// BGRA com tone mapping (preserva destaques HDR sem aspecto lavado).
		// Deve ocorrer ANTES da escala, pois ResizeBGRA espera BGRA 4 bytes/px.
		if frame.ColorSpace != 0 {
			tonemapped := screen.ToneMapHDRToSDR(frame)
			if !ownsFrame {
				s.capturer.ReleaseFrame()
			}
			frame = tonemapped
			ownsFrame = true
		}

		// ── Aplica escala (se necessario) ──
		scaleFactor := q.ScaleFactor
		if scaleFactor > 0 && scaleFactor < 1.0 {
			resized := screen.ResizeBGRA(frame, scaleFactor)
			if !ownsFrame {
				s.capturer.ReleaseFrame()
			}
			frame = resized
			ownsFrame = true
		}

		// ── Dirty rect detection (timing) ──
		// NOTA: go-d3d já faz HW dirty rects internamente em GetImage().
		// Este software diff é REDUNDANTE quando usando go-d3d, mas mantido
		// para fallback (DXGI manual/GDI). Em go-d3d, se a GPU disse que nada
		// mudou, GetImage retorna ErrNoImageYet (timeout) — já tratado acima.
		dirtyStart := time.Now()
		rects := s.dirtyDetector.Detect(frame)
		dirtyMs := float64(time.Since(dirtyStart).Microseconds()) / 1000.0
		dirtyMsTotal += dirtyMs

		if len(rects) == 0 && s.useDirtyRect {
			// Idle: nada mudou
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

		// ── Copia frame do GPU (timing) ──
		// NOTA: go-d3d retorna c.img.Pix (buffer Go reutilizado).
		// Precisamos copiar ANTES de enviar ao encode worker, pois o próximo
		// AcquireNextFrame() sobrescreve c.img.Pix.
		copyStart := time.Now()
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
		copyMs := float64(time.Since(copyStart).Microseconds()) / 1000.0
		copyMsTotal += copyMs
		s.useDirtyRect = true

		// Envia para encode worker (nao bloqueante com buffer=1)
		// KEY FRAME: o primeiro frame (ou quando o rect cobre a tela toda)
		// é enviado como frame COMPLETO (não tile) para o viewer ter a base.
		// Sem isso, o viewer fica com tela preta até a primeira mudança.
		var jobRects []screen.DirtyRect
		fullFrame := !s.useDirtyRect // primeiro frame = key frame
		if s.tileMode && len(rects) > 0 && !fullFrame {
			// Tile-mode: envia apenas os rects alterados (economia de banda).
			// Se o rect cobre ~100% da tela, envia como frame completo também
			// (mais eficiente que N tiles grandes).
			covered := 0
			for _, r := range rects {
				covered += r.Width * r.Height
			}
			totalPx := frame.Width * frame.Height
			if totalPx > 0 && covered >= totalPx*90/100 {
				fullFrame = true
			}
		}
		if s.tileMode && len(rects) > 0 && !fullFrame {
			jobRects = rects
		}
		select {
		case encodeChan <- encodeJob{
			frame:  frame,
			width:  frame.Width,
			height: frame.Height,
			seq:    s.frameSeq,
			rects:  jobRects,
		}:
		case <-ctx.Done():
			return ctx.Err()
		case <-s.stopCh:
			return nil
		}

		// ── Throttle real por FPS ──
		// curFps vem de q.EffectiveFps(): >0 = FPS máximo; 0 = sem limite.
		// Sem throttle, o agent captura o mais rápido possível (bug: enviava
		// mais frames que o definido). Aqui limitamos a taxa de captura.
		if curFps > 0 {
			frameInterval := time.Second / time.Duration(curFps)
			elapsed := time.Since(loopStart)
			if elapsed < frameInterval {
				select {
				case <-time.After(frameInterval - elapsed):
				case <-ctx.Done():
					return ctx.Err()
				case <-s.stopCh:
					return nil
				}
			}
		}
		loopStart = time.Now()
	}
}

// Stop encerra o loop de captura.
func (s *SessionScreen) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
	if s.cursorStopCh != nil {
		select {
		case <-s.cursorStopCh:
		default:
			close(s.cursorStopCh)
		}
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

// SetTileMode ativa/desativa o modo tile-based (apenas tiles alterados).
// Requer viewer compatível; desligado por padrão (backward compatible).
func (s *SessionScreen) SetTileMode(enabled bool) {
	s.tileMode = enabled
}

// SetCursorSeparate ativa/desativa o envio do cursor separado do frame.
// Quando ativo, o capturer não desenha o cursor no frame e a posição é
// publicada via subject .cursor (economia de banda em interações de mouse).
func (s *SessionScreen) SetCursorSeparate(enabled bool) {
	s.cursorSeparate = enabled
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
		"eventType":        "metrics",
		"effectiveFps":     q.EffectiveFps(),
		"profileFps":       q.Fps,
		"quality":          s.quality.Profile(),
		"imageQuality":     q.EffectiveJpegQuality(),
		"resolution":       fmt.Sprintf("%dx%d", width, height),
		"compressionRatio": compressionRatio,
		"avgEncodeMs":      avgEncodeMs,
		"frameBytesAvg":    int64(0),
		"framesSent5s":     framesSent,
		"framesSkipped5s":  framesSkipped,
		"totalFrames":      s.frameSeq,
	}
	if framesSent > 0 {
		metrics["frameBytesAvg"] = encBytes / framesSent
	}

	s.natsStream.PublishEvent(s.sessionID, "metrics", metrics)
}
