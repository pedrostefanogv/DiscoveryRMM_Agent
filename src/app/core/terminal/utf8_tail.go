package terminal

import "unicode/utf8"

// Utf8IncompleteTail retorna o número de bytes no FIM de b que formam uma
// runa UTF-8 INCOMPLETA (0 se b termina em runa completa/ASCII).
//
// Uso no terminal remoto: um chunk de output pode dividir um caractere
// multi-byte (ç, ã, é, emoji) no meio — despachar essa metade quebrada faz o
// viewer renderizar U+FFFD (mojibake), pois o TextDecoder do browser decodifica
// cada mensagem isoladamente. O caller retém esses bytes e os anexa ao início
// do próximo chunk.
//
// Lógica: procura nos últimos 4 bytes um byte INICIAL de runa (RuneStart);
// se a sequência iniciada por ele estende-se além do fim do buffer, esse
// sufixo está incompleto. Bytes de continuação órfãos/inválidos não são
// retidos (não é possível "completá-los" — passa direto).
//
// Exemplos: "abc"→0 · "x\xc3\xa9"→0 · "x\xc3"→1 · "\xf0\x9f"→2 · "\xf0\x9f\x9a"→3
// ("🚀" = \xf0\x9f\x9a\x80 dividida em 2, 3 e 4 bytes).
func Utf8IncompleteTail(b []byte) int {
	n := len(b)
	if n == 0 {
		return 0
	}
	max := 4
	if n < max {
		max = n
	}
	for k := 1; k <= max; k++ {
		lead := b[n-k]
		if !utf8.RuneStart(lead) {
			continue // byte de continuação — procura o início mais atrás
		}
		need := utf8SeqLen(lead)
		if need == 0 {
			return 0 // lead inválido — passa direto
		}
		if need > k {
			return k // runa iniciada mas incompleta
		}
		return 0 // sequência completa termina no fim do buffer
	}
	return 0
}

// utf8SeqLen retorna o comprimento esperado de uma sequência UTF-8 a partir
// do byte inicial (1=ASCII; 0 = byte que não pode iniciar runa).
func utf8SeqLen(lead byte) int {
	switch {
	case lead < 0x80:
		return 1
	case lead < 0xC0:
		return 0 // byte de continuação órfão
	case lead < 0xE0:
		return 2
	case lead < 0xF0:
		return 3
	case lead < 0xF8:
		return 4
	default:
		return 0
	}
}
