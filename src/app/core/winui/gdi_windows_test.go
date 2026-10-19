//go:build windows

package winui

import (
	"strings"
	"testing"
)

// TestMeasureWrappedText garante o cálculo de altura usado para dimensionar a
// caixa de mensagem do consentimento (textos longos crescem o cartão).
func TestMeasureWrappedText(t *testing.T) {
	font := CreateFont(15, 400)
	if font == 0 {
		t.Skip("CreateFont falhou neste ambiente")
	}
	defer DeleteGDIObject(font)

	short := MeasureWrappedText(font, "Olá", 400)
	if short <= 0 {
		t.Skip("MeasureWrappedText indisponível neste ambiente (sem DC)")
	}
	long := MeasureWrappedText(font, strings.Repeat("texto longo ", 40), 400)
	if long <= short {
		t.Fatalf("texto longo mediu %d, esperado > %d", long, short)
	}
	narrow := MeasureWrappedText(font, "uma frase razoavelmente longa para quebrar em varias linhas", 120)
	wide := MeasureWrappedText(font, "uma frase razoavelmente longa para quebrar em varias linhas", 600)
	if narrow <= wide {
		t.Fatalf("largura menor deveria medir mais alto (narrow=%d wide=%d)", narrow, wide)
	}
}
