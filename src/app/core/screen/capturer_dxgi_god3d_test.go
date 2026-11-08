//go:build windows

package screen

import (
	"errors"
	"testing"
)

// ── Testes da correção "primeiro frame parcial" do capturer go-d3d ────────────
//
// O capturer DXGI real exige GPU + desktop duplicável, então o que se testa aqui
// é a LÓGICA DE ESTADO que decide o caminho do primeiro frame — que é justamente
// onde estava o bug (delta sobre staging recém-criada). O capturer GDI é
// injetado por gdiFactory, exatamente como o channelCalibrator injeta a
// referência por refFn (ver channel_order.go), para cobrir o fluxo sem GPU.

// fakeFirstFrameCapturer é um Capturer de mentira usado no lugar do GDI. Conta
// quantas vezes foi aberto/capturado/fechado e devolve um frame de conteúdo
// conhecido, para verificar que o primeiro frame vem do caminho integral e que o
// capturer é fechado logo depois (não fica aberto junto com o DXGI).
type fakeFirstFrameCapturer struct {
	acquires int
	released int
	closed   int
	data     []byte
	width    int
	height   int
	originX  int
	originY  int
	err      error

	// zeraAoFechar reproduz o gdiCapturer REAL: Close() destrói a DIB section
	// (releaseCached → DeleteObject) e a slice entregue em Data passa a apontar
	// para memória liberada. Serve para provar que o capturer DXGI copia os
	// pixels antes de fechar o GDI — sem a cópia o consumidor leria lixo
	// (use-after-free, que em produção se manifestou como segfault).
	zeraAoFechar bool
}

func (f *fakeFirstFrameCapturer) AcquireNextFrame() (*Frame, error) {
	f.acquires++
	if f.err != nil {
		return nil, f.err
	}
	return &Frame{
		Data:    f.data,
		Width:   f.width,
		Height:  f.height,
		Stride:  f.width * 4,
		OriginX: f.originX,
		OriginY: f.originY,
	}, nil
}

func (f *fakeFirstFrameCapturer) ReleaseFrame() { f.released++ }

func (f *fakeFirstFrameCapturer) Close() error {
	f.closed++
	if f.zeraAoFechar {
		// Simula a memória liberada: quem guardou a slice vê lixo daqui em diante.
		for i := range f.data {
			f.data[i] = 0xEE
		}
	}
	return nil
}

func (f *fakeFirstFrameCapturer) Name() string { return "fake-gdi" }
func (f *fakeFirstFrameCapturer) Geometry() (int, int, int, int) {
	return f.originX, f.originY, f.width, f.height
}

// newCapturerComFake monta um dxgiGoD3dCapturer sem device/duplicação (não
// chamamos snapshotFrame nestes testes) apontando o primeiro frame para o fake.
// Devolve também o fake para inspeção.
//
// O fake recebe a MESMA geometria do output (c.width x c.height): é o que o
// gdiCapturer real produz, já que captureFirstFrameGDI o cria para a região
// exata do output. Assim o frame do fake é tão válido quanto o de produção e a
// validação de integridade do capturer não é mascarada pelo teste.
func newCapturerComFake(f *fakeFirstFrameCapturer) *dxgiGoD3dCapturer {
	c := &dxgiGoD3dCapturer{
		width:        8,
		height:       4,
		originX:      100,
		originY:      200,
		monitorIndex: 0,
		calib:        newChannelCalibrator(ChannelOrderBGRA),
	}
	f.width, f.height = c.width, c.height
	if f.data == nil {
		f.data = make([]byte, c.width*4*c.height)
	}
	c.gdiFactory = func(x, y, w, h int) (Capturer, error) {
		// A região pedida tem de ser a DESTE output (origem do DXGI), não o
		// índice de monitor: a ordem de EnumOutputs e EnumDisplayMonitors diverge.
		if x != c.originX || y != c.originY || w != c.width || h != c.height {
			return nil, errors.New("região pedida ao GDI difere da geometria do output")
		}
		return f, nil
	}
	return c
}

// TestFirstFrameUsaGDI cobre o núcleo da correção: com o primeiro frame
// pendente, o capturer entrega a captura GDI (quadro inteiro) e não o delta do
// DXGI — e fecha o capturer GDI em seguida para não manter dois abertos.
func TestFirstFrameUsaGDI(t *testing.T) {
	// Frame de tamanho real do output (8x4 = 128 bytes), com os 4 primeiros
	// pixels marcados para conferir que a cópia preserva o conteúdo byte a byte.
	pixels := make([]byte, 8*4*4)
	copy(pixels, []byte{1, 2, 3, 4})
	fake := &fakeFirstFrameCapturer{data: pixels, originX: 100, originY: 200}
	c := newCapturerComFake(fake)

	if c.FirstFrameIntegral() {
		t.Fatal("antes do primeiro frame, FirstFrameIntegral deve ser false")
	}

	frame, err := c.captureFirstFrameGDI()
	if err != nil {
		t.Fatalf("captureFirstFrameGDI falhou: %v", err)
	}
	if frame == nil {
		t.Fatal("frame nulo")
	}
	if fake.acquires != 1 {
		t.Fatalf("capturas GDI = %d, want 1", fake.acquires)
	}
	if fake.released != 1 {
		t.Fatalf("ReleaseFrame GDI = %d, want 1 (obrigatório após o uso)", fake.released)
	}
	if fake.closed != 1 {
		t.Fatalf("Close GDI = %d, want 1 (não manter dois capturers abertos)", fake.closed)
	}
	// A geometria do DXGI é a fonte da verdade (o InputController mapeia o mouse
	// com OriginX/OriginY).
	if frame.OriginX != 100 || frame.OriginY != 200 {
		t.Fatalf("origem = (%d,%d), want (100,200)", frame.OriginX, frame.OriginY)
	}
	// O frame GDI é BGRA canônico: NÃO pode passar por calib.apply (um swap aqui
	// inverteria R↔B e poderia contaminar o cache do processo).
	if frame.Data[0] != 1 || frame.Data[2] != 3 {
		t.Fatalf("frame GDI foi alterado: %v, want [1 2 3 4]", frame.Data[:4])
	}
}

// TestFirstFrameFalhaGDICaiNoDXGI garante que uma falha do GDI não derruba a
// captura: o estado avança (para não tentar GDI a cada frame) e o caminho volta
// ao comportamento antigo, com registro de diagnóstico.
func TestFirstFrameFalhaGDICaiNoDXGI(t *testing.T) {
	fake := &fakeFirstFrameCapturer{err: errors.New("BitBlt falhou")}
	c := newCapturerComFake(fake)

	if _, err := c.captureFirstFrameGDI(); err == nil {
		t.Fatal("esperava erro quando o GDI falha")
	}
	if fake.closed != 1 {
		t.Fatalf("capturer GDI deve ser fechado mesmo em erro, closed = %d", fake.closed)
	}
	if c.firstFrameDone {
		t.Fatal("captureFirstFrameGDI não deve marcar o primeiro frame como concluído")
	}
}

// TestResetDuplicationStateVoltaAPrimeiroFrame cobre a invariante mais fácil de
// quebrar: quando a duplicação é recriada (troca de monitor/resolução, perda de
// acesso DXGI), a staging volta a ser indefinida — o capturer precisa voltar a
// exigir o primeiro frame integral. Sem isso, o bug reaparece no meio da sessão.
func TestResetDuplicationStateVoltaAPrimeiroFrame(t *testing.T) {
	fake := &fakeFirstFrameCapturer{}
	c := newCapturerComFake(fake)

	c.firstFrameDone = true
	c.firstFrameViaGDI = true
	if !c.FirstFrameIntegral() {
		t.Fatal("estado armado deveria reportar primeiro frame integral")
	}

	c.resetDuplicationState()
	if c.firstFrameDone || c.firstFrameViaGDI {
		t.Fatalf("reset não limpou o estado: done=%v viaGDI=%v", c.firstFrameDone, c.firstFrameViaGDI)
	}
	if c.FirstFrameIntegral() {
		t.Fatal("após o reset, FirstFrameIntegral deve ser false")
	}
}

// TestFirstFrameIntegralFalsoQuandoGDIFalhou documenta a semântica do relatório
// de diagnóstico: o primeiro frame só conta como integral se veio do GDI.
func TestFirstFrameIntegralFalsoQuandoGDIFalhou(t *testing.T) {
	c := &dxgiGoD3dCapturer{width: 8, height: 4, calib: newChannelCalibrator(ChannelOrderBGRA)}
	// Simula o caminho de fallback de AcquireNextFrame quando o GDI falha.
	c.firstFrameDone = true
	c.firstFrameViaGDI = false
	if c.FirstFrameIntegral() {
		t.Fatal("primeiro frame via delta do DXGI não é integral")
	}
}

// TestGDIFactoryUsaRegiaoDoOutput garante que o primeiro frame é pedido à região
// exata DESTE output. Se pedíssemos por índice de monitor, a divergência de
// ordenação entre EnumOutputs (DXGI) e EnumDisplayMonitors (GDI) poderia capturar
// o monitor errado — o fake devolve erro nesse caso, então o teste falha se a
// geometria não for a do output.
func TestGDIFactoryUsaRegiaoDoOutput(t *testing.T) {
	fake := &fakeFirstFrameCapturer{}
	c := newCapturerComFake(fake)
	c.originX, c.originY = 1920, 0 // monitor secundário à direita
	if _, err := c.captureFirstFrameGDI(); err != nil {
		t.Fatalf("a região pedida ao GDI deve ser a do output: %v", err)
	}
}

// TestFirstFrameSobreviveAoCloseDoGDI é a regressão do use-after-free.
//
// O gdiCapturer entrega Data como slice SOBRE a memória da DIB section e
// Close() destrói essa DIB (releaseCached → DeleteObject). Se o capturer DXGI
// devolvesse o frame do GDI e fechasse o capturer em seguida, o consumidor
// leria memória liberada — foi exatamente assim que este bug se manifestou:
// segfault em TestCartaoRealNuncaVazaParaOFrame, que faz
// append([]byte(nil), frame.Data...) (banner_exclusion_capture_windows_test.go).
//
// O fake marca a memória ao fechar; o frame entregue tem de continuar íntegro.
func TestFirstFrameSobreviveAoCloseDoGDI(t *testing.T) {
	originais := make([]byte, 8*4*4) // geometria real do output (8x4)
	for i := range originais {
		originais[i] = byte(i%251 + 1) // valores não triviais, sem 0xEE
	}
	fake := &fakeFirstFrameCapturer{
		data:         append([]byte(nil), originais...),
		originX:      100,
		originY:      200,
		zeraAoFechar: true,
	}
	c := newCapturerComFake(fake)

	frame, err := c.captureFirstFrameGDI()
	if err != nil {
		t.Fatalf("captureFirstFrameGDI falhou: %v", err)
	}
	if fake.closed != 1 {
		t.Fatalf("capturer GDI deve ser fechado, closed = %d", fake.closed)
	}
	// O GDI já foi fechado (memória "liberada") — os pixels têm de estar
	// intactos porque foram copiados para um buffer do capturer DXGI.
	if len(frame.Data) != len(originais) {
		t.Fatalf("tamanho do frame = %d, want %d", len(frame.Data), len(originais))
	}
	for i, want := range originais {
		if frame.Data[i] != want {
			t.Fatalf("frame.Data[%d] = %d, want %d — o buffer do GDI foi usado depois do Close (use-after-free)",
				i, frame.Data[i], want)
		}
	}
	// O frame não pode compartilhar armazenamento com o buffer do GDI.
	if len(frame.Data) > 0 && &frame.Data[0] == &fake.data[0] {
		t.Fatal("frame.Data aliasing do buffer do GDI: deve ser uma cópia própria")
	}
}
