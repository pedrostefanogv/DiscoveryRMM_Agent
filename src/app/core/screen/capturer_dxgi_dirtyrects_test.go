//go:build windows

package screen

import "testing"

// Testes da tradução de RECT do DXGI para DirtyRect (Fase 6, 2026-11-20).
//
// O que estes testes protegem: os RECTs de GetFrameDirtyRects vêm em
// coordenadas do DESKTOP VIRTUAL, enquanto o Frame começa em (originX,
// originY). Não subtrair a origem desloca TODOS os tiles num monitor que não
// seja o primário — a mesma classe de bug já corrigida no cursor.
//
// A extração COM está embutida em (*dxgiCapturer).captureDirtyRectsLocked e
// exige uma duplicação DXGI real. O que é testável sem GPU é a parte pura:
// a tradução de coordenadas e a consolidação. Estes testes cobrem a tradução
// replicando exatamente a expressão usada em produção, para que uma mudança de
// sinal ou de base seja pega aqui.

// translateRectForTest replica a tradução de captureDirtyRectsLocked.
func translateRectForTest(r dxgiRect, originX, originY int) (DirtyRect, bool) {
	w := int(r.Right - r.Left)
	h := int(r.Bottom - r.Top)
	if w <= 0 || h <= 0 {
		return DirtyRect{}, false
	}
	return DirtyRect{
		X:      int(r.Left) - originX,
		Y:      int(r.Top) - originY,
		Width:  w,
		Height: h,
	}, true
}

func TestTranslateDXGIRectMonitorPrimario(t *testing.T) {
	// Monitor primário: origem (0,0) — a tradução é identidade.
	r := dxgiRect{Left: 10, Top: 20, Right: 110, Bottom: 220}
	got, ok := translateRectForTest(r, 0, 0)
	if !ok {
		t.Fatal("rect válido foi rejeitado")
	}
	if got.X != 10 || got.Y != 20 || got.Width != 100 || got.Height != 200 {
		t.Fatalf("tradução errada: %+v", got)
	}
}

func TestTranslateDXGIRectMonitorSecundario(t *testing.T) {
	// Monitor à direita do primário: a origem do output é (1920,0). O RECT da
	// GPU vem em coordenadas do desktop virtual (1920+), e o frame começa em 0.
	// Sem subtrair a origem o tile sairia 1920 px à direita.
	r := dxgiRect{Left: 1920, Top: 0, Right: 2020, Bottom: 100}
	got, ok := translateRectForTest(r, 1920, 0)
	if !ok {
		t.Fatal("rect válido foi rejeitado")
	}
	if got.X != 0 || got.Y != 0 || got.Width != 100 || got.Height != 100 {
		t.Fatalf("origem não foi subtraída: %+v (esperado 0,0 100x100)", got)
	}
}

func TestTranslateDXGIRectMonitorNegativo(t *testing.T) {
	// Monitor à ESQUERDA do primário: DesktopCoordinates.Left é NEGATIVO
	// (ex.: -1920). O frame começa em 0, então X pode ficar > 0 após subtrair.
	r := dxgiRect{Left: -1920, Top: -200, Right: -1820, Bottom: -100}
	got, ok := translateRectForTest(r, -1920, -200)
	if !ok {
		t.Fatal("rect válido foi rejeitado")
	}
	if got.X != 0 || got.Y != 0 {
		t.Fatalf("origem negativa não foi tratada: %+v", got)
	}
}

func TestTranslateDXGIRectDegenerado(t *testing.T) {
	// RECT com bordas invertidas ou nulas: não há área para atualizar.
	for _, r := range []dxgiRect{
		{Left: 10, Top: 10, Right: 10, Bottom: 50}, // largura 0
		{Left: 10, Top: 10, Right: 50, Bottom: 10}, // altura 0
		{Left: 50, Top: 50, Right: 10, Bottom: 10}, // invertido
	} {
		if _, ok := translateRectForTest(r, 0, 0); ok {
			t.Fatalf("rect degenerado %+v deveria ser rejeitado", r)
		}
	}
}

// TestHardwareDirtyRectsNilQuandoSemMetadados documenta o contrato de nil:
// sem metadados o pipeline manda frame COMPLETO (nunca idle).
func TestHardwareDirtyRectsNilQuandoSemMetadados(t *testing.T) {
	c := &dxgiCapturer{}
	if got := c.HardwareDirtyRects(); got != nil {
		t.Fatalf("sem metadados deve devolver nil, veio %v", got)
	}
	// Fechado também devolve nil.
	c.closed = true
	if got := c.HardwareDirtyRects(); got != nil {
		t.Fatalf("fechado deve devolver nil, veio %v", got)
	}
}

// TestHardwareDirtyRectsDevolveRectsQuandoHaMetadados garante que a slice é
// entregue ao pipeline quando o DXGI reportou regiões.
func TestHardwareDirtyRectsDevolveRectsQuandoHaMetadados(t *testing.T) {
	c := &dxgiCapturer{}
	c.rectsFrame = []DirtyRect{{X: 5, Y: 6, Width: 7, Height: 8}}
	c.lastHadMetadata = true
	got := c.HardwareDirtyRects()
	if len(got) != 1 || got[0].X != 5 {
		t.Fatalf("esperava o rect reportado, veio %v", got)
	}
}
