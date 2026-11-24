//go:build windows

package remotesession

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"log"
	"sync"
	"sync/atomic"
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
	// inputLock: bloqueio (KVM lock) da entrada da máquina remota. atomic.Pointer
	// porque o .control é assinado antes de runScreenSession anexar o manager —
	// um comando do viewer pode chegar nesse intervalo e deve ser ignorado (não
	// corromper a leitura).
	inputLock atomic.Pointer[InputLockManager]
	// annotations: overlay transparente click-through para desenhos do técnico.
	annotations atomic.Pointer[screen.AnnotationOverlay]

	// Dirty rect detection
	dirtyDetector *screen.DirtyDetector
	useDirtyRect  bool // ativado apos primeiro frame completo
	// hwDirtyRects: true quando o capturer ATIVO decide por hardware se a tela
	// mudou (DXGI go-d3d/manual). Nesse caso o diff de SOFTWARE é PULADO — o
	// capturer já bloqueou esperando uma mudança e devolveu "sem frame" quando
	// a tela estava parada. Pagar a varredura de tiles (0,4-2,0 ms @1080p,
	// medido em bench_test.go) depois de o hardware já ter respondido é custo
	// puro, e era o maior custo de CPU do caminho quente. Reavaliado a cada
	// troca de capturer (GDI↔DXGI), porque a capacidade muda com o capturer.
	hwDirtyRects bool

	// forceKeyFrame: quando true, o próximo frame capturado é enviado COMPLETO
	// (key frame), ignorando dirty rects/tiles. Usado quando o viewer volta a
	// ficar visível (troca de aba do navegador) — o canvas pode ter sido limpo
	// e o viewer precisa da tela inteira para redesenhar sem esperar mudanças.
	forceKeyFrame atomic.Bool

	// Capturer: em desktop normal, DXGI Desktop Duplication é o PRIMÁRIO
	// (captura por GPU, suporte a HDR e dirty rects). GDI é o fallback em VMs
	// sem GPU/sessões onde DXGI falha e o primário exclusivo em desktops
	// seguros (Winlogon/UAC/lock, onde DXGI é bloqueado pela sessão). Em
	// desktop normal a troca GDI↔DXGI é bidirecional.
	capturerMu        sync.Mutex
	monitorIndex      int
	drawCursor        bool
	usingDXGIFallback bool // true = capturer atual é DXGI (fallback dinâmico GDI↔DXGI)
	captureFailures   int  // falhas consecutivas da captura GDI (fallback DXGI)
	// lastSecureKeepLog: throttle do log "mantendo GDI em desktop seguro" —
	// o estado pode persistir minutos (logon aberto com GDI falhando) e sem
	// throttle o threshold (40 falhas ≈ 2s) logaria 30 linhas/minuto.
	lastSecureKeepLog time.Time

	tileMode bool // quando true, envia apenas os tiles alterados (EncodeDirtyRects)

	// tileScratch: buffers REUTILIZÁVEIS da codificação por tiles. Vive na sessão
	// (não no worker) para sobreviver entre frames: sem ele, cada frame em tile
	// mode alocava ~900 objetos e ~16 MB (medido em BenchmarkEncodeDirtyRects,
	// Fase 0) — pressão de GC constante durante toda a sessão, competindo com a
	// captura. O encode é sequencial na sessão, então não há corrida.
	tileScratch screen.TileScratch

	// Gate da ordem de canais (R↔B): enquanto o capturer não decidiu se os
	// bytes estão em BGRA ou RGBA, os frames são DESCARTADOS (não publicados).
	// Publicar com a ordem errada mostra a tela com vermelho/azul trocados —
	// incidente de 2026-10-29 (DESKTOP-DH608V7): ~68 s de cores invertidas
	// porque o frame ia para o viewer enquanto a calibração decidia.
	// channelGateUntil limita a espera (a sessão não pode ficar preta se a
	// calibração nunca concluir — ex.: tela acromática com GDI indisponível).
	channelGateCap     screen.Capturer
	channelGateUntil   time.Time
	channelGateExpired bool // já logou/passou do deadline deste capturer
	channelGateBlocked bool // frames estão sendo segurados agora

	// bandwidthGate: teto DURO de banda que PAUSA a captura (não é qualidade).
	// Complementa a escada AUTO: a escada reduz a compressão, mas sem frear a
	// produção a fila ainda cresce. Ver bandwidth_gate.go.
	bandwidthGate *BandwidthGate

	// Cursor separado: frame não contém cursor; posição/estado via subject .cursor
	cursorSeparate bool
	cursorSender   *screen.CursorSpriteSender
	cursorStopCh   chan struct{}

	frameSeq        uint64
	webrtcSendFrame func([]byte) bool
	stopCh          chan struct{}
	doneCh          chan struct{}

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

	// channelGateMaxWait: tempo MÁXIMO que a publicação fica segurada esperando
	// a ordem de canais (R↔B) ser decidida. Curto de propósito: com a decisão
	// determinística (sonda do DuplicateOutput1 + cache) a ordem já chega
	// pronta; este prazo cobre só o caminho de calibração por GDI (que pode
	// falhar em VMs). Depois dele o frame é publicado com a melhor informação
	// disponível — e a calibração continua tentando corrigir.
	channelGateMaxWait = 1500 * time.Millisecond
)

// SetWebRtcSendFrame registra o envio opcional de frames pelo data channel WebRTC.
// Quando retorna false (canal ainda não aberto, buffer cheio ou acima do teto SCTP),
// o consumer cai automaticamente para PublishFrame no NATS.
func (s *SessionScreen) SetWebRtcSendFrame(fn func([]byte) bool) {
	if s == nil {
		return
	}
	s.webrtcSendFrame = fn
}

// NewSessionScreen cria uma nova sessao de screen capture.
// monitorIndex permite selecionar um monitor (0 = primário); use -1 para o padrão.
func NewSessionScreen(sessionID string, natsStream *NatsStreamHandler) (*SessionScreen, error) {
	return NewSessionScreenMonitor(sessionID, natsStream, 0)
}

// NewSessionScreenMonitor cria uma sessao de screen capture de um monitor específico.
func NewSessionScreenMonitor(sessionID string, natsStream *NatsStreamHandler, monitorIndex int) (*SessionScreen, error) {
	// DPI awareness: garante coordenadas FÍSICAS (GetSystemMetrics/
	// EnumDisplayMonitors/GetCursorPos/SendInput não-virtualizados) mesmo em
	// processos sem manifest PerMonitorV2 (serviço, spawns). Sem isso, com
	// escala 125%/150% as métricas lógicas se misturam com os frames físicos
	// e o input/cursor do acesso remoto desloca. Idempotente.
	screen.EnsurePerMonitorDPIAwareness()

	// Diagnóstico de formato de cor: a calibração R↔B dos capturers
	// (channel_order.go) registra a decisão no log unificado — sem isso não dá
	// para saber, olhando o log de uma sessão, qual ordem de canais foi usada.
	screen.SetDebugLogger(func(msg string) {
		log.Printf("[remote-session-screen] %s\n", msg)
	})

	// Capturer: DXGI-first em desktop normal (!IsSecureDesktop) via
	// NewCapturerMode(monitorIndex, !cursorSeparate) para hardware dirty-rects,
	// HDR e menor uso de CPU; GDI em desktop seguro (Winlogon/UAC — onde DXGI
	// congela/preto) e como fallback quando DXGI não está disponível.
	_, _ = screen.CheckDesktopSwitch()
	const defaultCursorSeparate = true
	var capturer screen.Capturer
	var err error
	if !screen.IsSecureDesktop() {
		capturer, err = screen.NewCapturerMode(monitorIndex, !defaultCursorSeparate)
		if err != nil {
			capturer, err = screen.NewGDICapturerMonitor(monitorIndex)
		}
	} else {
		capturer, err = screen.NewGDICapturerMonitor(monitorIndex)
		if err != nil {
			if _, aerr := screen.CheckDesktopSwitch(); aerr == nil {
				capturer, err = screen.NewGDICapturerMonitor(monitorIndex)
			}
			if err != nil {
				capturer, err = screen.NewCapturerMode(monitorIndex, !defaultCursorSeparate)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("screen capture: %w", err)
	}
	usingDXGI := capturer.Name() == "dxgi"

	// Log de geometria (diagnóstico DPI/multi-monitor): região capturada
	// dentro do desktop virtual físico. Se captura ≠ desktop inteiro, o
	// InputController mapeia o mouse com origem+escala (mapFrameToAbsolute).
	gx, gy, gw, gh := capturer.Geometry()
	vx, vy, vw, vh, vok := screen.VirtualDesktopBounds()
	if vok {
		log.Printf("[remote-session-screen] geometria: captura %dx%d @(%d,%d) | desktop virtual %dx%d @(%d,%d) | dpi=%s\n",
			gw, gh, gx, gy, vw, vh, vx, vy, screen.DpiAwarenessSource())
	} else {
		log.Printf("[remote-session-screen] geometria: captura %dx%d @(%d,%d) | desktop virtual indisponível\n", gw, gh, gx, gy)
	}
	desktopName := screen.CurrentDesktopName()
	if desktopName == "" {
		desktopName = "(inacessível)"
	}
	log.Printf("[remote-session-screen] desktop ativo: %s%s\n", desktopName,
		map[bool]string{true: " (seguro — logon/UAC, captura GDI)", false: ""}[screen.IsSecureDesktop()])

	encoder := screen.NewJPEGEncoder()
	codecSel := NewCodecSelector()
	quality := NewQualityManager(QualityConfig{})
	recording := NewRecordingSource(sessionID, natsStream)
	inputCtrl := NewInputController(sessionID)

	return &SessionScreen{
		sessionID:         sessionID,
		capturer:          capturer,
		monitorIndex:      monitorIndex,
		usingDXGIFallback: usingDXGI,
		encoder:           encoder,
		natsStream:        natsStream,
		quality:           &quality,
		codecSel:          codecSel,
		recording:         recording,
		inputCtrl:         inputCtrl,
		// Teto de banda DESLIGADO por padrão (CeilingKbps=0): preserva o
		// comportamento atual. Ligar exige um limite explícito do operador.
		bandwidthGate:   NewBandwidthGate(defaultBandwidthGateConfig()),
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
	// NOTA: fecha o capturer VIGENTE no fim do Start (capturado no momento
	// da execução do defer, não no registro) — se o loop trocou para o
	// fallback DXGI, o capturer original já foi fechado no swap e o vigente
	// é fechado aqui.
	defer func() {
		s.capturerMu.Lock()
		c := s.capturer
		s.capturerMu.Unlock()
		if c != nil {
			_ = c.Close()
		}
	}()

	// fps: >0 = máximo; 0 = sem limite. Se ausente, usa o perfil.
	if fps <= 0 {
		fps = s.quality.Current().EffectiveFps()
	}

	// Capacidade de dirty rects de hardware do capturer inicial (DXGI go-d3d/
	// manual = sim; GDI = não). Define se o loop paga o diff de software.
	_ = s.refreshHardwareDirtyRects(s.currentCapturer())

	log.Printf("[remote-session-screen] Start: fps=%d capturer=%s hw-dirty-rects=%v\n",
		fps, s.currentCapturer().Name(), s.hwDirtyRects)

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
					// CORREÇÃO (cursor em monitor não-primário): GetCursorPos retorna
					// coordenadas do DESKTOP VIRTUAL, mas o viewer desenha o sprite no
					// canvas do FRAME (frame-relativo). Sem subtrair a origem da
					// captura, o cursor desloca sempre que a sessão captura um monitor
					// com origem != (0,0) — segundo monitor, monitor à esquerda, etc.
					if cap := s.currentCapturer(); cap != nil {
						gx, gy, _, _ := cap.Geometry()
						info.X = clampCursorCoord(int(info.X) - gx)
						info.Y = clampCursorCoord(int(info.Y) - gy)
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
		pooled bool               // frame veio do pool de frames (devolver após encode)

		// Métricas de input: capW/capH = região coberta pela CAPTURA no desktop
		// (ANTES da eventual escala — ResizeBGRA), originX/originY = origem da
		// captura no desktop virtual físico. Alimentam o mapeamento do mouse
		// (UpdateFrameMetrics/UpdateFrameOrigin); com escala<1 o frame é menor
		// que a captura e capW/capH mantêm a cobertura real.
		capW, capH       int
		originX, originY int
	}

	type frameResult struct {
		data             []byte
		seq              uint64
		width            int
		height           int
		rawBytes         int64
		encodeMs         float64
		capW, capH       int
		originX, originY int
		// inTiles: este frame foi enviado como DELTA (tiles) e não como imagem
		// completa. Alimenta a telemetria de tiles= no log (Fase 6).
		inTiles bool
	}

	encodeChan := make(chan encodeJob, 1) // buffer=1: captura nunca bloqueia se encoder ocupado
	resultChan := make(chan frameResult, 2)

	// encodeFree sinaliza que o worker de encode LIBEROU o slot (o item saiu do
	// encodeChan). Substitui o polling de 1 ms do produtor.
	//
	// Por que isto existe (Fase 5 da otimização do acesso remoto, 2026-11-19):
	// o produtor fazia "if len(encodeChan) >= 1 { time.Sleep(1ms); continue }".
	// O sleep de 1 ms é busy-wait: sob carga (exatamente quando o encoder
	// satura, que é quando esta espera acontece) o produtor acorda ~1000x por
	// segundo só para checar de novo. Com o sinal, o produtor DORME até o
	// worker liberar o slot e acorda imediatamente — sem atraso de até 1 ms no
	// caminho, e sem consumo de CPU ocioso.
	//
	// Capacidade 1 + envio não-bloqueante: o sinal é um "aviso", não uma fila.
	// Se o produtor ainda não estiver esperando, o aviso fica guardado e ele
	// não dorme à toa na próxima volta.
	encodeFree := make(chan struct{}, 1)

	var encodeWg sync.WaitGroup
	encodeWg.Add(1)
	go func() {
		defer encodeWg.Done()
		for job := range encodeChan {
			// O item saiu da fila: o slot está livre para a captura. Avisa o
			// produtor (não-bloqueante: um aviso pendente já basta).
			select {
			case encodeFree <- struct{}{}:
			default:
			}

			encStart := time.Now()
			enc := s.getEncoder()
			quality := s.quality.Current().EffectiveJpegQuality()
			rawLen := int64(len(job.frame.Data))
			var encoded []byte
			if job.rects != nil {
				// Modo tile: codifica apenas os rects alterados (economia de banda).
				// O scratch é DA SESSÃO (s.tileScratch) e sobrevive entre frames —
				// sem ele cada frame alocava ~900 objetos/16 MB em tile mode.
				// Usa o codec ativo (webp/jpeg) para economizar mais ~30-45% nos tiles.
				tiles, err := screen.EncodeDirtyRectsCodecScratch(job.frame, job.rects, quality, enc.Name(), &s.tileScratch)
				if err != nil {
					log.Printf("[remote-session-screen] ERRO encode tiles: %v\n", err)
					if job.pooled {
						screen.PutPooledFrame(job.frame)
					}
					continue
				}
				encoded = tiles
			} else {
				var err error
				encoded, err = enc.Encode(job.frame, quality)
				if err != nil {
					log.Printf("[remote-session-screen] ERRO encode: %v\n", err)
					if job.pooled {
						screen.PutPooledFrame(job.frame)
					}
					continue
				}
			}
			// Devolve o frame ao pool após o uso (otimização de churn de GC:
			// o buffer ~8 MB é reciclado para o próximo copy em vez de novo make).
			if job.pooled {
				screen.PutPooledFrame(job.frame)
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
				rawBytes: rawLen,
				encodeMs: encMs,
				capW:     job.capW,
				capH:     job.capH,
				originX:  job.originX,
				originY:  job.originY,
				inTiles:  job.rects != nil,
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

	// Contadores do caminho de envio, por janela de métricas.
	//
	// Sem eles não havia como distinguir, lendo o log, "o data channel está
	// aberto mas a tela continua no NATS" de "a tela está mesmo em P2P": era o
	// ponto cego que manteve o WebRTC inerte em produção — peer "connected",
	// canais abertos, zero frames por eles. Atômicos porque o consumer escreve e
	// o loop de métricas (outra goroutine) lê.
	var webrtcFrames, natsFrames, webrtcTooLarge, webrtcNotReady atomic.Int64
	var webrtcFirstFrame atomic.Bool

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for job := range frameChan {
			// Escolha EXPLÍCITA do canal: o data channel de mídia é o caminho
			// preferido quando está aberto; o NATS é fallback, não o padrão.
			//
			// Distinguir "sem canal" de "acima do teto SCTP" é deliberado: o
			// primeiro é transitório (negociação em curso) e o segundo é
			// estrutural (aquele frame NUNCA passará pelo data channel). Os dois
			// caem para o NATS, mas pedem ações diferentes de quem lê o log.
			if s.webrtcSendFrame != nil {
				if s.webrtcSendFrame(job.data) {
					webrtcFrames.Add(1)
					if webrtcFirstFrame.CompareAndSwap(false, true) {
						log.Printf("[remote-session-screen] primeiro frame enviado por WebRTC (data channel media) session=%s\n", s.sessionID)
					}
					continue
				}
				if len(job.data) > maxWebRtcMessageBytes {
					webrtcTooLarge.Add(1)
				} else {
					webrtcNotReady.Add(1)
				}
			}
			if err := s.natsStream.PublishFrame(s.sessionID, job.data); err != nil {
				log.Printf("[remote-session-screen] ERRO ao publicar frame: %v\n", err)
			} else {
				natsFrames.Add(1)
			}
		}
	}()

	// ── Producer loop (captura rapida, sem bloqueio de encode) ──
	var totalSent, totalSkipped, totalIdle int64
	// gateWaits/totalGateWaits: voltas do loop em que a captura foi PAUSADA
	// pelo teto de banda (observabilidade — distingue "rede freou" de
	// "máquina lenta"/"tela ociosa" no log de métricas).
	var gateWaits, totalGateWaits int64
	// sentInTiles / hwRectFrames: observabilidade do caminho de dirty rects de
	// hardware (Fase 6). sentInTiles conta frames enviados em TILES (delta);
	// hwRectFrames conta frames em que o capturer REPORTOU regiões. Se
	// hwRectFrames fica em 0 com um capturer DXGI, a otimização não está
	// engatando e o motivo precisa ser investigado.
	var sentInTiles, hwRectFrames int64
	// totalHwRects acumula a sessão inteira (o log zera a cada 10 s; o evento de
	// métricas precisa de um número que não reinicie para a UI mostrar tendência).
	var totalHwRects int64
	var sent, skipped int64
	var rawBytes, encBytes int64
	var encodeTimeTotal time.Duration
	var captureMsTotal, dirtyMsTotal, copyMsTotal, maskMsTotal float64
	consecutiveIdle := 0
	curFps := fps
	loopStart := time.Now()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[remote-session-screen] ctx cancelado: %d env, %d skip, %d idle, %d gate\n",
				totalSent, totalSkipped, totalIdle, totalGateWaits)
			s.natsStream.PublishEvent(s.sessionID, "screen_stopped",
				map[string]string{"reason": "context_cancelled"})
			close(encodeChan)
			encodeWg.Wait()
			close(resultChan)
			close(frameChan)
			wg.Wait()
			return ctx.Err()

		case <-s.stopCh:
			log.Printf("[remote-session-screen] stop: %d env, %d skip, %d idle, %d gate\n",
				totalSent, totalSkipped, totalIdle, totalGateWaits)
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

			// Métricas do mapeamento do mouse: frame codificado (width/height),
			// cobertura real da captura (capW/capH, difere com escala) e a
			// origem da captura no desktop virtual físico (troca GDI→DXGI muda
			// origem/dimensão — atualiza a cada frame).
			s.inputCtrl.UpdateFrameMetrics(result.width, result.height, result.capW, result.capH)
			s.inputCtrl.UpdateFrameOrigin(result.originX, result.originY)
			s.recording.CaptureFrame(result.data)
			s.frameSeq++
			if result.inTiles {
				sentInTiles++
			}
			s.quality.RecordFrame(len(result.data), time.Now())

			rawBytes += result.rawBytes
			encBytes += int64(len(result.data) - frameHeaderLen)
			encodeTimeTotal += time.Duration(result.encodeMs * float64(time.Millisecond))
			sent++
			totalSent++

			// Metricas a cada 5s
			if time.Since(s.lastMetricsTime) >= 5*time.Second {
				s.publishMetrics(result.width, result.height, sent, skipped, rawBytes, encBytes, encodeTimeTotal, sentInTiles, hwRectFrames, totalHwRects)
				s.lastMetricsTime = time.Now()
			}

			// Log detalhado a cada 10s — timing de cada etapa do pipeline
			if time.Since(s.lastLogTime) >= 10*time.Second {
				compRatio := float64(0)
				avgEncMs := float64(0)
				avgCapMs := float64(0)
				avgDirtyMs := float64(0)
				avgCopyMs := float64(0)
				avgMaskMs := float64(0)
				if rawBytes > 0 {
					compRatio = float64(rawBytes) / float64(encBytes)
				}
				if sent > 0 {
					avgEncMs = float64(encodeTimeTotal.Milliseconds()) / float64(sent)
					avgCapMs = captureMsTotal / float64(sent)
					avgDirtyMs = dirtyMsTotal / float64(sent)
					avgCopyMs = copyMsTotal / float64(sent)
					avgMaskMs = maskMsTotal / float64(sent)
				}
				// mask= é o custo da reescrita da máscara do banner (0 quando o
				// capturer contorna a região por conta própria ou não há cartão).
				// dirty= passa a ser ~0 no caminho DXGI, que decide por hardware
				// (ver hw-dirty-rects= no log de Start).
				// hwrects=/tiles= (Fase 6, 2026-11-20): sem estes dois números não
				// havia como saber, olhando o log, se o caminho de dirty rects de
				// HARDWARE estava de fato alimentando o tile-mode. Era exatamente o
				// ponto cego que deixou o tile-mode inerte em produção sem ninguém
				// notar: o log dizia "tile-mode=true" e todo frame saía completo.
				// tx= é o indicador decisivo do transporte da TELA: sem estes números, um
				// data channel aberto e ocioso era indistinguível de P2P funcionando.
				// grande= e semcanal= explicam um nats= alto sem esconder a causa.
				// Swap(0) em vez de Load+Store: o consumer incrementa em paralelo, e um
				// frame contado entre a leitura e o reset se perderia na janela seguinte
				// (subcontagem silenciosa justamente no numero que prova o P2P).
				wf, nf := webrtcFrames.Swap(0), natsFrames.Swap(0)
				wLarge, wNotReady := webrtcTooLarge.Swap(0), webrtcNotReady.Swap(0)
				log.Printf("[remote-session-screen] %d env, %d idle, %d gate | comp %.1f:1 | cap=%.1fms dirty=%.1fms copy=%.1fms mask=%.1fms enc=%.1fms | tiles=%d hwrects=%d | tx webrtc=%d nats=%d grande=%d semcanal=%d | fps=%d\n",
					sent, totalIdle, gateWaits, compRatio, avgCapMs, avgDirtyMs, avgCopyMs, avgMaskMs, avgEncMs,
					sentInTiles, hwRectFrames, wf, nf, wLarge, wNotReady, curFps)
				// (reset ja feito pelo Swap acima)
				s.lastLogTime = time.Now()
				sent, skipped = 0, 0
				sentInTiles, hwRectFrames = 0, 0
				gateWaits = 0
				rawBytes, encBytes = 0, 0
				encodeTimeTotal = 0
				captureMsTotal, dirtyMsTotal, copyMsTotal, maskMsTotal = 0, 0, 0, 0
			}
			continue

		default:
		}

		q := s.quality.Current()
		curFps = q.EffectiveFps()

		// ── Desktop switch (base: MeshAgent CheckDesktopSwitch) ──
		// Anexa o thread ao input desktop ativo e detecta troca (logon screen,
		// lock screen, UAC/secure desktop). Sem isso, a captura continua no
		// desktop antigo (tela congelada) e o SendInput falha no secure desktop.
		if switched, derr := screen.CheckDesktopSwitch(); derr == nil && switched {
			log.Printf("[remote-session-screen] troca de desktop detectada (%s) — forçando key frame\n", screen.CurrentDesktopName())
			// Força key frame: reseta o dirty detector e desativa tile-mode
			// temporariamente para o próximo frame ir COMPLETO ao viewer.
			s.dirtyDetector.Reset()
			s.useDirtyRect = false
		}

		// Recovery frame: depois de a qualidade voltar ao topo, envia UM frame
		// completo para limpar o artefato de compressão que ficou na tela do
		// viewer. Reseta o dirty detector (o próximo frame é integral) sem
		// desligar o tile-mode de forma permanente.
		if s.consumeRecoveryFrame() {
			s.dirtyDetector.Reset()
			s.forceKeyFrame.Store(true)
			s.useDirtyRect = false
			log.Printf("[remote-session-screen] recovery frame: qualidade recuperada, reenviando tela completa\n")
		}

		// Backpressure: nao captura se encoder estiver ocupado (buffer=1 cheio).
		//
		// Espera por SINAL (encodeFree), não polling: o worker avisa quando
		// retira o job da fila. O select com ctx/stop mantém o encerramento
		// responsivo — dormir aqui nunca pode atrasar o Stop da sessão.
		if len(encodeChan) >= 1 {
			select {
			case <-encodeFree:
				// Slot liberado: segue para capturar imediatamente.
			case <-ctx.Done():
				return ctx.Err()
			case <-s.stopCh:
				return nil
			}
			continue
		}

		// ── Teto de banda: PAUSA a produção de frames (não é qualidade) ──
		// Complementa o backpressure do encoder acima. Aquele protege contra a
		// CPU (encoder ocupado); este protege contra a REDE (link não escoa).
		// Sem ele, baixar a qualidade não impede a fila de crescer: o frame
		// entregue fica cada vez mais velho.
		// A espera é limitada por idleDelay e reeavaliada a cada volta — o
		// gate reabre sozinho quando o viewer reporta banda recuperada.
		if s.bandwidthGate != nil && s.bandwidthGate.Paused() {
			gateWaits++
			totalGateWaits++
			time.Sleep(idleDelay)
			continue
		}

		// ── Capture (timing) ──
		cap := s.currentCapturer()
		capStart := time.Now()
		frame, err := cap.AcquireNextFrame()
		capMs := float64(time.Since(capStart).Microseconds()) / 1000.0
		if err != nil {
			skipped++
			totalSkipped++
			// Fallback one-way GDI→DXGI: se o capturer atual (GDI) falhar
			// repetidamente, troca para DXGI e permanece nele (sem volta).
			s.maybeFallbackToDXGI(err)
			time.Sleep(idleDelay)
			continue
		}
		s.captureFailures = 0
		captureMsTotal += capMs

		// ── Gate da ordem de canais (R↔B) ──
		// Não publica o frame antes de o capturer saber se os bytes estão em
		// BGRA (contrato do pipeline) ou RGBA (precisa swap). Publicar com a
		// ordem errada = tela com vermelho/azul trocados até a calibração
		// concluir (incidente de 2026-10-29, ~68 s).
		if s.channelOrderGate(cap) {
			cap.ReleaseFrame()
			// Ao sair do gate o próximo frame precisa ir COMPLETO.
			s.forceKeyFrame.Store(true)
			skipped++
			totalSkipped++
			time.Sleep(idleDelay)
			continue
		}

		// Métricas de input capturadas ANTES do tone mapping/escala: a
		// cobertura real da captura no desktop virtual e a origem dela. O
		// tone mapping/ResizeBGRA recriam o Frame (OriginX/OriginY zerados
		// nos frames gerados), então guardamos os valores do frame original.
		inputCapW, inputCapH := frame.Width, frame.Height
		inputOriginX, inputOriginY := frame.OriginX, frame.OriginY

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
				cap.ReleaseFrame()
			} else {
				screen.PutPooledFrame(frame)
			}
			frame = tonemapped
			ownsFrame = true
		}

		// ── Aplica escala (se necessario) ──
		scaleFactor := q.ScaleFactor
		if scaleFactor > 0 && scaleFactor < 1.0 {
			if !ownsFrame && screen.RegionMaskAvailable() {
				// Copia para buffer do pool antes de aplicar a máscara para não
				// mutar o buffer interno do capturer DXGI.
				frameCopy := screen.GetPooledFrame(frame.Width, frame.Height, frame.Stride)
				if len(frameCopy.Data) != len(frame.Data) {
					frameCopy.Data = frameCopy.Data[:len(frame.Data)]
				}
				copy(frameCopy.Data, frame.Data)
				frameCopy.Width = frame.Width
				frameCopy.Height = frame.Height
				frameCopy.Stride = frame.Stride
				cap.ReleaseFrame()
				frame = frameCopy
				ownsFrame = true
			}
			maskStart := time.Now()
			screen.PatchFrameBannerIfNeededAt(cap, frame, inputOriginX, inputOriginY)
			maskMsTotal += float64(time.Since(maskStart).Microseconds()) / 1000.0

			resized := screen.ResizeBGRA(frame, scaleFactor)
			if !ownsFrame {
				cap.ReleaseFrame()
			} else {
				screen.PutPooledFrame(frame)
			}
			frame = resized
			ownsFrame = true
		}

		// ── Dirty rect detection (timing) ──
		// Quando o capturer decide por HARDWARE se a tela mudou (DXGI go-d3d e
		// manual), o diff de software é PULADO: o capturer já bloqueou esperando
		// uma mudança e devolveu "sem frame" quando nada mudou (tratado no
		// caminho de erro acima). Refazer a varredura de tiles aqui era custo
		// puro — medido em 0,4 ms (tudo mudou) a 2,0 ms (tela ociosa, o regime
		// mais comum) por frame a 1080p, segundo bench_test.go.
		//
		// O detector é MANTIDO nos fallbacks (GDI, e DXGI que não reporte a
		// capacidade): lá a decisão de "mudou" só existe por software.
		//
		// `rects` nil com hwDirtyRects=true significa "não sei as regiões, mas a
		// tela mudou" — o frame vai COMPLETO (decideFrameKind devolve jobRects
		// nil), que é o comportamento correto e seguro.
		//
		// ATUALIZAÇÃO Fase 6 (2026-11-20): quando o capturer implementa
		// DirtyRectsReporter (DXGI manual e go-d3d), rects JÁ VEM preenchido
		// pelo hardware e o tile-mode passa a valer neste caminho. Antes desta
		// mudança o comentário acima descrevia um tile-mode permanentemente
		// inerte no caminho de produção.
		dirtyStart := time.Now()
		var rects []screen.DirtyRect
		if !s.hwDirtyRects {
			rects = s.dirtyDetector.Detect(frame)
		} else if hw, ok := cap.(screen.DirtyRectsReporter); ok {
			// Fase 6 (2026-11-20): o capturer sabe ONDE a tela mudou, de graça.
			// Sem isto o tile-mode ficava inerte no caminho DXGI (rects=nil),
			// e todo frame saía como JPEG da tela inteira — medido em produção
			// a 3440x1440, ~5 MP recomprimidos por quadro mesmo com a tela
			// quase parada. Ver DirtyRectsReporter em core/screen.
			rects = hw.HardwareDirtyRects()
			if len(rects) > 0 {
				hwRectFrames++
				totalHwRects++
				if scaleFactor > 0 && scaleFactor < 1.0 {
					rects = scaleDirtyRects(rects, scaleFactor, frame.Width, frame.Height)
				}
			}
		}
		dirtyMs := float64(time.Since(dirtyStart).Microseconds()) / 1000.0
		dirtyMsTotal += dirtyMs

		// Keyframe solicitado (viewer voltou a ficar visível): NÃO pula o frame
		// mesmo com dirty rects vazio — se a tela remota estiver parada, é o
		// único jeito de redesenhar o canvas do viewer (que pode ter sido
		// descartado pelo browser). O flag é consumido mais abaixo, ao montar
		// o job de encode como frame completo.
		keyframePending := s.forceKeyFrame.Load()

		if frameIsIdle(s.hwDirtyRects, rects, s.useDirtyRect, keyframePending) {
			// Idle: nada mudou (ver frameIsIdle — com hwDirtyRects isto NUNCA
			// dispara: um frame RETORNADO pelo capturer já é uma mudança).
			if !ownsFrame {
				cap.ReleaseFrame()
			} else {
				screen.PutPooledFrame(frame)
			}
			totalIdle++
			consecutiveIdle++
			if consecutiveIdle > deepIdleThreshold {
				time.Sleep(idleDelay * 2)
			}
			continue
		}
		consecutiveIdle = 0

		// Decide se o frame vai COMPLETO ou em TILES ANTES de copiar do capturer:
		// em tile-mode copiamos apenas os dirty rects (~KB em vez de ~20 MB).
		fullFrame, jobRects := s.decideFrameKind(frame, rects)

		// ── Copia frame do GPU (timing) ──
		// NOTA: go-d3d/dxgi reutilizam o buffer interno entre chamadas.
		// Precisamos copiar ANTES de enviar ao encode worker, pois o próximo
		// AcquireNextFrame() sobrescreve o buffer.
		copyStart := time.Now()
		// Both paths yield a pooled frame: ToneMapHDRToSDR/ResizeBGRA allocate
		// from framePool (ownsFrame=true) and !ownsFrame copies into framePool.
		jobPooled := true
		if !ownsFrame {
			// Pool de frames (otimização de churn de GC): o encode worker devolve
			// o frame ao pool após o encode.
			frameCopy := screen.GetPooledFrame(frame.Width, frame.Height, frame.Stride)
			if len(frameCopy.Data) != len(frame.Data) {
				frameCopy.Data = frameCopy.Data[:len(frame.Data)]
			}
			if !fullFrame && len(jobRects) > 0 {
				screen.CopyFrameRects(frameCopy, frame, jobRects)
			} else {
				copy(frameCopy.Data, frame.Data)
			}
			frameCopy.Width = frame.Width
			frameCopy.Height = frame.Height
			frameCopy.Stride = frame.Stride
			cap.ReleaseFrame()
			frame = frameCopy
			ownsFrame = true
		}
		copyMs := float64(time.Since(copyStart).Microseconds()) / 1000.0
		copyMsTotal += copyMs

		// Aplica a máscara do banner APÓS copiar para o frame do pool (nunca
		// mutando o buffer interno do capturer DXGI) quando não houve ResizeBGRA.
		if !(scaleFactor > 0 && scaleFactor < 1.0) {
			maskStart := time.Now()
			screen.PatchFrameBannerIfNeededAt(cap, frame, inputOriginX, inputOriginY)
			maskMsTotal += float64(time.Since(maskStart).Microseconds()) / 1000.0
		}

		// Envia para encode worker (nao bloqueante com buffer=1).
		select {
		case encodeChan <- encodeJob{
			frame:   frame,
			width:   frame.Width,
			height:  frame.Height,
			seq:     s.frameSeq,
			rects:   jobRects,
			pooled:  jobPooled,
			capW:    inputCapW,
			capH:    inputCapH,
			originX: inputOriginX,
			originY: inputOriginY,
		}:
		case <-ctx.Done():
			// Frame poolado não entregue: devolve ao pool.
			if jobPooled {
				screen.PutPooledFrame(frame)
			}
			return ctx.Err()
		case <-s.stopCh:
			if jobPooled {
				screen.PutPooledFrame(frame)
			}
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

func scaleDirtyRects(rects []screen.DirtyRect, scale float64, maxW, maxH int) []screen.DirtyRect {
	out := make([]screen.DirtyRect, 0, len(rects))
	for _, r := range rects {
		x := int(float64(r.X) * scale)
		y := int(float64(r.Y) * scale)
		w := int(float64(r.Width)*scale) + 2
		h := int(float64(r.Height)*scale) + 2
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x+w > maxW {
			w = maxW - x
		}
		if y+h > maxH {
			h = maxH - y
		}
		if w > 0 && h > 0 {
			out = append(out, screen.DirtyRect{X: x, Y: y, Width: w, Height: h})
		}
	}
	return out
}

// frameIsIdle decide se o frame recém-capturado deve ser DESCARTADO por não
// haver mudança (tela ociosa). Função PURA, extraída do loop para ser testável:
// o bug que ela já escondeu (revisão 2, 2026-11-19) justifica a cerimônia.
//
// Semântica dos parâmetros:
//   - hwDirtyRects: o capturer ativo decide por HARDWARE se a tela mudou. Nesse
//     caso um frame RETORNADO significa "o hardware viu mudança" — o caso de
//     tela ociosa chega como ERRO de timeout no AcquireNextFrame, nunca como
//     frame. Retornar true aqui descartaria TODOS os frames após o primeiro e
//     congelaria o stream na primeira imagem (foi exatamente o bug que esta
//     função veio matar: o gate antigo via rects==nil do caminho HW e lia
//     "idle", contradizendo o comentário que prometia frame completo).
//   - rects: saída do diff de software (nil no caminho HW).
//   - useDirtyRect: false no primeiro frame (ainda não há base comparativa).
//   - keyframePending: pedido de redesenho completo do viewer — nunca idle.
func frameIsIdle(hwDirtyRects bool, rects []screen.DirtyRect, useDirtyRect, keyframePending bool) bool {
	if hwDirtyRects {
		return false
	}
	return len(rects) == 0 && useDirtyRect && !keyframePending
}

// decideFrameKind decide COMO o frame capturado será enviado ao viewer:
// frame COMPLETO (JPEG da tela inteira, jobRects == nil) ou em TILES
// (dirty rects). Devolve (fullFrame, jobRects).
//
// Regras, em ordem:
//  1. PRIMEIRO frame publicado por esta sessão/capturer é sempre COMPLETO —
//     é a base sobre a qual o viewer desenha os deltas seguintes. Sem isso o
//     viewer não tem onde aplicar os tiles e mostra tela preta/em pedaços.
//  2. forceKeyFrame armado (viewer voltou à aba, saída do gate de canais,
//     recovery frame, troca de desktop) força COMPLETO e descarta os tiles
//     pendentes. O flag é consumido aqui (CompareAndSwap).
//  3. Em tile-mode, se os rects cobrirem ~100% da tela sai mais barato
//     mandar o frame inteiro do que N tiles grandes.
//  4. Caso contrário, tile-mode manda só os rects alterados.
//
// ORDEM IMPORTA (bug corrigido em 2026-11-07): a flag useDirtyRect PRECISA ser
// lida para decidir o frame CORRENTE e só então virar true. O código anterior
// fazia "s.useDirtyRect = true" e depois "fullFrame := !s.useDirtyRect" — a
// expressão era sempre false, o comentário "primeiro frame = key frame" era
// código morto, e o primeiro frame só saía completo por acidente (flag de
// keyframe armada ou rects cobrindo >=90%). Sintoma relatado pelo operador:
// ao conectar, a tela remota chegava com faixas pretas e só ia "carregando"
// conforme o mouse gerava novos dirty rects. Ver
// session_screen_firstframe_test.go (regressão).
func (s *SessionScreen) decideFrameKind(frame *screen.Frame, rects []screen.DirtyRect) (bool, []screen.DirtyRect) {
	var jobRects []screen.DirtyRect
	firstFrame := !s.useDirtyRect // primeira publicação desta sessão/capturer
	fullFrame := firstFrame
	// A partir daqui o diff pode ser usado — o frame atual já é a base.
	s.useDirtyRect = true

	if s.forceKeyFrame.CompareAndSwap(true, false) {
		fullFrame = true
		// Remove tiles/dirty rects pendentes — o frame vai inteiro.
		jobRects = nil
	}

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

	return fullFrame, jobRects
}

// clampCursorCoord limita uma coordenada de cursor (desktop - origem) ao
// range int16 usado no Encode do sprite. Valores negativos (cursor fora da
// região capturada, ex.: no outro monitor) são preservados — o viewer
// simplesmente não desenha fora do canvas.
func clampCursorCoord(v int) int16 {
	const minI16 = -32768
	const maxI16 = 32767
	if v < minI16 {
		return minI16
	}
	if v > maxI16 {
		return maxI16
	}
	return int16(v)
}

// channelOrderGate decide se o frame recém-capturado deve ser DESCARTADO
// porque a ordem dos canais de cor (R↔B) ainda não foi decidida. Enquanto o
// capturer não sabe se os bytes estão em BGRA ou RGBA, publicar significa
// mostrar a tela com vermelho/azul trocados (incidente de 2026-10-29: ~68 s de
// cores invertidas até a calibração concluir).
//
// A espera é limitada por channelGateMaxWait e reiniciada a cada troca de
// capturer (GDI↔DXGI), porque o capturer novo recomeça a decisão.
func (s *SessionScreen) channelOrderGate(cap screen.Capturer) bool {
	reporter, hasReporter := cap.(screen.ChannelOrderReporter)
	pending := hasReporter && reporter.ChannelOrderPending()

	// Capturer trocado (ou primeiro frame): reabre a janela de espera.
	if s.channelGateCap != cap {
		s.channelGateCap = cap
		s.channelGateExpired = false
		s.channelGateBlocked = false // estado do capturer ANTERIOR não se aplica
		s.channelGateUntil = time.Now().Add(channelGateMaxWait)
		if pending {
			log.Printf("[remote-session-screen] ordem de canais (R↔B) pendente no capturer %s — segurando a publicação por até %s\n",
				cap.Name(), channelGateMaxWait)
		}
	}

	if !pending {
		if s.channelGateBlocked || s.channelGateExpired {
			s.channelGateBlocked = false
			s.channelGateExpired = false
			order := "n/a"
			if hasReporter {
				order = reporter.ChannelOrderName()
			}
			log.Printf("[remote-session-screen] ordem de canais resolvida (%s) — retomando a publicação\n", order)
			// O primeiro frame publicado depois do gate precisa ser COMPLETO
			// (o viewer pode ter sido criado sem base de tela).
			s.forceKeyFrame.Store(true)
		}
		return false
	}

	if time.Now().After(s.channelGateUntil) {
		if !s.channelGateExpired {
			s.channelGateExpired = true
			log.Printf("[remote-session-screen] ordem de canais (R↔B) ainda pendente após %s — publicando com %s; a calibração corrige em seguida se estiver errada\n",
				channelGateMaxWait, reporter.ChannelOrderName())
		}
		return false
	}

	s.channelGateBlocked = true
	return true
}

// maybeFallbackToDXGI troca o capturer após falhas consecutivas de captura
// (threshold ~2s com idleDelay 50ms). A partir da correção de auto-ajuste de
// geometria, a lógica é BIDIRECIONAL: se o capturer ATIVO for DXGI e passar a
// falhar consecutivamente (ex.: DXGI_ERROR_ACCESS_LOST após troca de
// resolução/DPI/monitor com a sessão ativa — a duplicação não sobrevive a
// mode change), recria o GDI com a geometria ATUAL, recuperando a sessão sem
// reinício manual. Se o ambiente não suportar GDI, as falhas acumulam de novo
// e ele volta a tentar DXGI — recuperação alternada, sem ping-pong rápido
// (40 falhas ≈ 2s de carência a cada troca).
func (s *SessionScreen) maybeFallbackToDXGI(lastErr error) {
	s.capturerMu.Lock()
	usingDXGI := s.usingDXGIFallback
	s.capturerMu.Unlock()

	s.captureFailures++
	if s.captureFailures < 40 {
		return
	}
	count := s.captureFailures
	s.captureFailures = 0
	if usingDXGI {
		log.Printf("[remote-session-screen] %d falhas consecutivas de captura dxgi (%v) — recriando GDI com geometria atual\n", count, lastErr)
		s.swapCapturer("gdi", "falhas consecutivas da captura DXGI (possível mudança de resolução/DPI)")
		return
	}

	// GDI → DXGI: NÃO trocar em desktop seguro (tela de logon, lock screen,
	// UAC). A Desktop Duplication API (DXGI) não entrega conteúdo confiável
	// nesses desktops (frames congelados/preto — ver screen.IsSecureDesktop);
	// o caminho que funciona lá é GDI BitBlt com o thread anexado via
	// SetThreadDesktop (padrão MeshAgent). Falhas de GDI aqui são tipicamente
	// transitórias (troca de desktop) — mantém o GDI tentando e loga com
	// throttle (o estado pode persistir minutos; log a cada 2s inundaria o
	// agent-service.log).
	if screen.IsSecureDesktop() {
		if time.Since(s.lastSecureKeepLog) >= 30*time.Second {
			s.lastSecureKeepLog = time.Now()
			log.Printf("[remote-session-screen] %d falhas de captura GDI em desktop seguro (%s) — mantendo GDI (DXGI não captura logon/UAC)\n",
				count, screen.CurrentDesktopName())
		}
		return
	}
	log.Printf("[remote-session-screen] %d falhas consecutivas de captura GDI (%v) — fallback para DXGI\n", count, lastErr)
	s.swapCapturer("dxgi", "falhas consecutivas da captura GDI")
}

// swapCapturer troca o capturer ativo fechando o anterior. O novo capturer
// é criado no thread atual (já anexado ao desktop alvo pelo
// CheckDesktopSwitch) — LockOSThread dos construtores respeita isso.
func (s *SessionScreen) swapCapturer(kind, reason string) {
	s.capturerMu.Lock()
	old := s.capturer
	s.capturerMu.Unlock()

	var newCap screen.Capturer
	var err error
	if kind == "gdi" {
		newCap, err = screen.NewGDICapturerMonitor(s.monitorIndex)
	} else {
		newCap, err = screen.NewCapturerMode(s.monitorIndex, false)
	}
	if err != nil {
		// Mantém o capturer atual — tenta de novo no próximo ciclo.
		log.Printf("[remote-session-screen] falha ao trocar capturer para %s (%s): %v\n", kind, reason, err)
		return
	}

	if old != nil {
		_ = old.Close()
	}

	s.capturerMu.Lock()
	s.capturer = newCap
	s.usingDXGIFallback = kind == "dxgi"
	s.capturerMu.Unlock()

	if ov := s.annotations.Load(); ov != nil && newCap != nil {
		gx, gy, gw, gh := newCap.Geometry()
		var oldRect image.Rectangle
		if old != nil {
			ox, oy, ow, oh := old.Geometry()
			oldRect = image.Rect(ox, oy, ox+ow, oy+oh)
		}
		newRect := image.Rect(gx, gy, gx+gw, gy+gh)
		if newRect != oldRect {
			ov.SetBounds(newRect)
		}
	}

	// A capacidade de dirty rects de hardware muda com o capturer (GDI não tem;
	// DXGI tem). Reavaliar ANTES de resetar o detector: se o novo capturer
	// decide por hardware, o diff de software deixa de ser necessário.
	s.refreshHardwareDirtyRects(newCap)

	// Troca de capturer ⇒ possível mudança de resolução/formato: força key
	// frame completo para o viewer re-sincronizar.
	s.dirtyDetector.Reset()
	s.useDirtyRect = false

	log.Printf("[remote-session-screen] capturer trocado para %s (%s) | hw-dirty-rects=%v\n", newCap.Name(), reason, s.hwDirtyRects)
}

// RequestKeyFrame força o próximo frame a ser enviado completo (key frame).
// Chamado quando o viewer volta a ficar visível (ex.: usuário retorna à aba
// do navegador) — o canvas pode ter sido descartado pelo browser e sem isso
// a tela ficaria preta até a próxima mudança de pixels na máquina remota.
func (s *SessionScreen) RequestKeyFrame() {
	s.forceKeyFrame.Store(true)
	log.Printf("[remote-session-screen] keyframe solicitado pelo viewer (sessionId=%s)\n", s.sessionID)
}

// currentCapturer retorna o capturer ativo (thread-safe).
func (s *SessionScreen) currentCapturer() screen.Capturer {
	s.capturerMu.Lock()
	defer s.capturerMu.Unlock()
	return s.capturer
}

// refreshHardwareDirtyRects recalcula se o capturer ATIVO decide por hardware
// que a tela mudou (ver HardwareDirtyRectsReporter em core/screen). Chamado
// quando a capacidade pode ter mudado: início da sessão e em toda troca de
// capturer (swapCapturer). É barato e roda no loop de captura, então não pode
// alocar nem chamar o Windows — só uma consulta à interface.
//
// Devolve se houve MUDANÇA, para o chamador logar uma única vez por transição
// (o loop roda a 30 fps; logar por frame inundaria o agent-service.log).
func (s *SessionScreen) refreshHardwareDirtyRects(cap screen.Capturer) bool {
	hw := false
	if cap != nil {
		if r, ok := cap.(screen.HardwareDirtyRectsReporter); ok {
			hw = r.HasHardwareDirtyRects()
		}
	}
	if hw == s.hwDirtyRects {
		return false
	}
	s.hwDirtyRects = hw
	return true
}

// SetInputLock anexa o gerenciador de bloqueio de entrada da sessão.
func (s *SessionScreen) SetInputLock(m *InputLockManager) {
	s.inputLock.Store(m)
}

// InputLock devolve o gerenciador de bloqueio (nil quando ainda não anexado).
func (s *SessionScreen) InputLock() *InputLockManager {
	return s.inputLock.Load()
}

// SetAnnotationOverlay anexa o overlay de anotações visuais da sessão.
func (s *SessionScreen) SetAnnotationOverlay(ov *screen.AnnotationOverlay) {
	s.annotations.Store(ov)
}

// AnnotationOverlay devolve o overlay de anotações (nil quando não anexado).
func (s *SessionScreen) AnnotationOverlay() *screen.AnnotationOverlay {
	return s.annotations.Load()
}

// Geometry devolve a geometria física (x, y, w, h) do monitor capturado.
func (s *SessionScreen) Geometry() (int, int, int, int) {
	cap := s.currentCapturer()
	if cap == nil {
		return 0, 0, 0, 0
	}
	return cap.Geometry()
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

	// FAIL-SAFE do KVM lock: a sessão está encerrando por QUALQUER motivo
	// (stop, liveness, erro) — devolve teclado/mouse ao usuário da máquina
	// remota ANTES de qualquer outra limpeza, para o intervalo sem controle
	// ser o menor possível. Idempotente.
	if lk := s.inputLock.Load(); lk != nil {
		lk.Close()
	}

	if ov := s.annotations.Load(); ov != nil {
		ov.Close()
	}

	// Libera teclas/modificadores que tenham ficado "presas" no remoto
	// (viewer fechou com a tecla segurada e o keyup nunca chegou) e encerra
	// o watchdog do input controller. Idempotente.
	if s.inputCtrl != nil {
		s.inputCtrl.Close()
	}
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
//
// Fase 6 (2026-11-20): quando o codec pedido não está disponível, o erro é
// PUBLICADO no log e o encoder de fallback (JPEG) é aplicado. Antes a troca era
// silenciosa: o log de start dizia "codec=webp" e o operador não tinha como
// saber se o pedido foi honrado — exatamente o antipadrão do H.264.
func (s *SessionScreen) SetCodec(codec string) {
	s.codecSel.SetPreferred(codec)
	encoder, err := s.codecSel.SelectChecked(codec)
	if err != nil {
		log.Printf("[remote-session-screen] codec solicitado não aplicado: %v (sessionId=%s)\n", err, s.sessionID)
	}
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

// SetImageQuality define a compressão da imagem manualmente (viewer).
// Aceita 10-90 (clampeado); sobrescreve o perfil e desliga a adaptação
// automática enquanto o modo manual estiver ativo.
func (s *SessionScreen) SetImageQuality(q int) {
	if q >= 1 && q <= 100 {
		s.quality.SetImageQuality(q)
	}
}

// SetImageQualityAuto aplica a qualidade do perfil em modo automático,
// sem o clamp manual de 10-90 (perfis legítimos: ultralow=25 … ultra=92).
func (s *SessionScreen) SetImageQualityAuto(q int) {
	if q >= 1 && q <= 100 {
		s.quality.SetImageQualityAuto(q)
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

// SetManualMode liga/desliga o modo manual (desabilita a adaptação automática).
// Reflete o flag de modo enviado pelo viewer (auto/manual).
func (s *SessionScreen) SetManualMode(manual bool) {
	s.quality.SetManualMode(manual)
}

// UpdateNetworkMetrics recebe as métricas de rede medidas no VIEWER
// (netstats via .input) e alimenta a escada adaptativa do modo auto.
func (s *SessionScreen) UpdateNetworkMetrics(rttMs, recvKbps float64, recvFrames int) {
	s.quality.UpdateNetworkMetrics(rttMs, recvKbps, recvFrames)

	// Alimenta o teto de banda. É um controle SEPARADO da qualidade: a escada
	// reduz a compressão, o gate PAUSA a produção quando o link não escoa.
	if s.bandwidthGate != nil {
		s.bandwidthGate.Observe(recvKbps, recvFrames)
	}
}

// ResetAutoToProfile volta à qualidade do perfil e re-semeia a escada
// automática (usado quando o viewer retorna ao modo auto sem override).
func (s *SessionScreen) ResetAutoToProfile() {
	s.quality.ResetAutoToProfile()
}

// ConfigureBandwidth aplica a política de teto de banda em runtime (comando
// "bandwidth" do canal .control). Os limites (AutoMin/Max/Step/ServerMax) são
// atualizados ANTES do modo, porque a troca de modo já os consulta para
// clampar o teto. Campo numérico <= 0 significa "preservar o corrente".
func (s *SessionScreen) ConfigureBandwidth(cfg BandwidthGateConfig) {
	if s.bandwidthGate == nil {
		return
	}
	if cfg.AutoMinKbps > 0 || cfg.AutoMaxKbps > 0 || cfg.AutoStepKbps > 0 || cfg.ServerMaxKbps > 0 {
		s.bandwidthGate.SetLimits(cfg.AutoMinKbps, cfg.AutoMaxKbps, cfg.AutoStepKbps, cfg.ServerMaxKbps)
	}
	ceiling := cfg.CeilingKbps
	if ceiling < 0 {
		ceiling = 0
	}
	s.bandwidthGate.SetMode(cfg.Mode, ceiling)
}

// BandwidthSnapshot devolve o estado atual do gate de banda (log/UI).
func (s *SessionScreen) BandwidthSnapshot() BandwidthSnapshot {
	if s.bandwidthGate == nil {
		return BandwidthSnapshot{}
	}
	return s.bandwidthGate.Snapshot()
}

// consumeRecoveryFrame decide se este frame deve ser um RECOVERY FRAME: um
// frame COMPLETO em alta qualidade, enviado uma vez após a qualidade voltar ao
// topo, para limpar o artefato que ficou na tela do viewer enquanto esteve
// degradada (padrão ControlR, FrameBasedCapturer.cs). Sem isso, uma região
// degradada permanece borrada até algo mudar naquele pedaço da tela.
func (s *SessionScreen) consumeRecoveryFrame() bool {
	if s.quality == nil {
		return false
	}
	return s.quality.ConsumeRecoveryFrame()
}

// publishMetrics publica metricas de streaming no subject .event para o viewer.
func (s *SessionScreen) publishMetrics(width, height int, framesSent, framesSkipped int64, rawBytes, encBytes int64, encodeTimeTotal time.Duration, tilesFrames, hwRects, totalHwRects int64) {
	q := s.quality.Current()
	compressionRatio := float64(0)
	avgEncodeMs := float64(0)
	if rawBytes > 0 {
		compressionRatio = float64(rawBytes) / float64(encBytes)
	}
	if framesSent > 0 {
		avgEncodeMs = float64(encodeTimeTotal.Milliseconds()) / float64(framesSent)
	}

	// Estado do teto de banda (kbps): no modo AUTO o teto se move sozinho, e o
	// viewer mostra o valor corrente. Vai junto das métricas (mesmo evento) para
	// não criar um subject novo por um dado que já é periódico.
	bandwidth := s.BandwidthSnapshot()

	metrics := map[string]any{
		"eventType":        "metrics",
		"bandwidth":        bandwidth,
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
		// tiles5s / hwDirtyRects5s (Fase 6, 2026-11-20): prova, no próprio
		// evento de métricas, que o delta por hardware está engatando. Sem
		// isso a única evidência seria o log do agente — e a ausência da
		// otimização passou meses despercebida justamente por não haver número.
		"tilesFrames5s":     tilesFrames,
		"hwDirtyRects5s":    hwRects,
		"hwDirtyRectsTotal": totalHwRects,
	}
	if framesSent > 0 {
		metrics["frameBytesAvg"] = encBytes / framesSent
	}

	s.natsStream.PublishEvent(s.sessionID, "metrics", metrics)
}
