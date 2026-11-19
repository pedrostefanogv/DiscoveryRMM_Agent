package winregistry

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// TestEncodeDecode_RoundTrip e o teste central do codec.
//
// Um editor de registro que corrompe dados e pior que nao existir: o operador
// grava, o Windows aceita, e a aplicacao quebra depois sem relacao obvia com a
// edicao. Estes casos cobrem exatamente os pontos onde codecs ingenuos falham.
func TestEncodeDecode_RoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		vType  ValueType
		input  string
		expect []byte
	}{
		{
			name:   "REG_SZ simples",
			vType:  TypeString,
			input:  "C:\\Program Files\\App",
			expect: encodeUTF16String("C:\\Program Files\\App"),
		},
		{
			name:   "REG_SZ vazio",
			vType:  TypeString,
			input:  "",
			expect: encodeUTF16String(""),
		},
		{
			name:   "REG_SZ com acentos",
			vType:  TypeString,
			input:  "Configuracao de Rede - Acao",
			expect: encodeUTF16String("Configuracao de Rede - Acao"),
		},
		{
			name:   "REG_SZ com emoji (fora do BMP)",
			vType:  TypeString,
			input:  "valor U0001F600 fim",
			expect: encodeUTF16String("valor U0001F600 fim"),
		},
		{
			name:   "REG_EXPAND_SZ",
			vType:  TypeExpandString,
			input:  "%SystemRoot%\\System32",
			expect: encodeUTF16String("%SystemRoot%\\System32"),
		},
		{
			name:   "REG_DWORD zero",
			vType:  TypeDWORD,
			input:  "0",
			expect: []byte{0x00, 0x00, 0x00, 0x00},
		},
		{
			name:   "REG_DWORD 1",
			vType:  TypeDWORD,
			input:  "1",
			expect: []byte{0x01, 0x00, 0x00, 0x00},
		},
		{
			name:   "REG_DWORD maximo (0xFFFFFFFF)",
			vType:  TypeDWORD,
			input:  "4294967295",
			expect: []byte{0xFF, 0xFF, 0xFF, 0xFF},
		},
		{
			name:   "REG_DWORD hexadecimal",
			vType:  TypeDWORD,
			input:  "0xFF",
			expect: []byte{0xFF, 0x00, 0x00, 0x00},
		},
		{
			name:   "REG_QWORD acima de 2^53",
			vType:  TypeQWORD,
			input:  "9007199254740993", // 2^53 + 1: perdido se trafegar como number JSON
			expect: []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x20, 0x00},
		},
		{
			name:   "REG_MULTI_SZ simples",
			vType:  TypeMultiString,
			input:  "a\nb\nc",
			expect: encodeUTF16MultiString([]string{"a", "b", "c"}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeValue(tc.vType, tc.input)
			if err != nil {
				t.Fatalf("DecodeValue erro: %v", err)
			}
			if !bytes.Equal(got, tc.expect) {
				t.Errorf("bytes inesperados\n  got: %v\n want: %v", got, tc.expect)
			}
		})
	}
}

// TestEncodeValue_DWORDMaximo verifica que 0xFFFFFFFF e devolvido como
// 4294967295, nao como -1. O registro trata DWORD como SEM SINAL; tratar como
// int32 assinado e o erro mais comum e produz um valor que o operador nao
// reconhece.
func TestEncodeValue_DWORDMaximo(t *testing.T) {
	entry := EncodeValue("Flag", 4, []byte{0xFF, 0xFF, 0xFF, 0xFF})
	if entry.Type != TypeDWORD {
		t.Fatalf("tipo = %s; queria %s", entry.Type, TypeDWORD)
	}
	if entry.Data != "4294967295" {
		t.Errorf("data = %q; queria \"4294967295\" (nao -1)", entry.Data)
	}
}

// TestEncodeValue_QWORDUnsigned cobre o caso que quebra em JavaScript:
// o protocolo carrega o valor como STRING porque o console (JS) perde precisao
// acima de 2^53.
func TestEncodeValue_QWORDUnsigned(t *testing.T) {
	// 0xFFFFFFFFFFFFFFFF
	entry := EncodeValue("Big", 11, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	if entry.Type != TypeQWORD {
		t.Fatalf("tipo = %s; queria %s", entry.Type, TypeQWORD)
	}
	if entry.Data != "18446744073709551615" {
		t.Errorf("data = %q; queria 18446744073709551615", entry.Data)
	}
}

// TestEncodeValue_BinaryPreservesBytes garante base64 byte-exato.
//
// REG_BINARY contem bytes que NAO sao UTF-8 valido por definicao. Converter
// para string perde bytes de forma silenciosa e irreversivel — o bug classico.
func TestEncodeValue_BinaryPreservesBytes(t *testing.T) {
	// Bytes que nao formam UTF-8 valido, incluindo NUL.
	raw := []byte{0x00, 0xFF, 0xFE, 0x80, 0x01, 0x00, 0xC3}
	entry := EncodeValue("Blob", 3, raw)

	if entry.Type != TypeBinary {
		t.Fatalf("tipo = %s; queria %s", entry.Type, TypeBinary)
	}
	decoded, err := base64.StdEncoding.DecodeString(entry.Data)
	if err != nil {
		t.Fatalf("data nao e base64 valido: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Errorf("round-trip perdeu bytes\n  got: %v\n want: %v", decoded, raw)
	}

	// E o caminho de volta (edicao) tambem precisa ser exato.
	back, err := DecodeValue(TypeBinary, entry.Data)
	if err != nil {
		t.Fatalf("DecodeValue erro: %v", err)
	}
	if !bytes.Equal(back, raw) {
		t.Errorf("DecodeValue perdeu bytes\n  got: %v\n want: %v", back, raw)
	}
}

// TestEncodeValue_UnknownTypePreservesData confirma que um tipo desconhecido
// nao descarta o dado: vira REG_NONE com base64. Perder o dado seria pior que
// nao saber interpreta-lo.
func TestEncodeValue_UnknownTypePreservesData(t *testing.T) {
	raw := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	entry := EncodeValue("Estranho", 9999, raw)
	if entry.Type != TypeNone {
		t.Errorf("tipo = %s; queria %s para tipo desconhecido", entry.Type, TypeNone)
	}
	decoded, err := base64.StdEncoding.DecodeString(entry.Data)
	if err != nil || !bytes.Equal(decoded, raw) {
		t.Error("tipo desconhecido deveria preservar os bytes em base64")
	}
}

// TestEncodeDecode_MultiStringPreservesEmptyEntry e um caso deliberadamente
// cruel: REG_MULTI_SZ com string VAZIA no meio.
//
// Um split ingenuo por NUL perde a entrada vazia, e "a\n\nb" viraria "a","b".
// O dado e significativo (a posicao importa).
func TestEncodeDecode_MultiStringPreservesEmptyEntry(t *testing.T) {
	original := []string{"primeiro", "", "terceiro"}
	raw := encodeUTF16MultiString(original)

	entry := EncodeValue("Lista", 7, raw)
	if len(entry.StringList) != 3 {
		t.Fatalf("StringList = %v (%d itens); queria 3 itens", entry.StringList, len(entry.StringList))
	}
	if entry.StringList[0] != "primeiro" || entry.StringList[1] != "" || entry.StringList[2] != "terceiro" {
		t.Errorf("StringList = %v; a entrada vazia do meio foi perdida", entry.StringList)
	}
}

// TestEncode_DWORDShortData: dado menor que 4 bytes nao pode ser lido como
// DWORD. Cair em base64 preserva o dado em vez de inventar um numero.
func TestEncode_DWORDShortData(t *testing.T) {
	entry := EncodeValue("Curto", 4, []byte{0x01, 0x02})
	if entry.Type != TypeDWORD {
		t.Fatalf("tipo = %s", entry.Type)
	}
	if _, err := base64.StdEncoding.DecodeString(entry.Data); err != nil {
		t.Errorf("dado curto deveria cair em base64, veio %q", entry.Data)
	}
}

// TestDecodeValue_RejectsNegative cobre a decisao de RECUSAR negativo.
//
// "-1" e o erro classico de quem vem de linguagem com inteiro assinado. Aceitar
// silenciosamente gravaria 0xFFFFFFFF, que pode nao ser o que o operador quis.
func TestDecodeValue_RejectsNegative(t *testing.T) {
	for _, v := range []string{"-1", "-100"} {
		if _, err := DecodeValue(TypeDWORD, v); err != ErrInvalidData {
			t.Errorf("DecodeValue(DWORD, %q) err = %v; queria ErrInvalidData", v, err)
		}
		if _, err := DecodeValue(TypeQWORD, v); err != ErrInvalidData {
			t.Errorf("DecodeValue(QWORD, %q) err = %v; queria ErrInvalidData", v, err)
		}
	}
}

// TestDecodeValue_RejectsGarbage confirma que dado invalido nao vira 0 em
// silencio: gravar 0 onde o operador digitou lixo e um bug silencioso.
func TestDecodeValue_RejectsGarbage(t *testing.T) {
	for _, v := range []string{"", "  ", "abc", "12abc", "0x", "1.5"} {
		if _, err := DecodeValue(TypeDWORD, v); err == nil {
			t.Errorf("DecodeValue(DWORD, %q) deveria falhar", v)
		}
	}
}

// TestDecodeValue_HexVariants confirma as grafias de hexadecimal aceitas.
func TestDecodeValue_HexVariants(t *testing.T) {
	// Notacao hexadecimal exige marcador explicito (0x ou sufixo h). "1F" puro
	// NAO e aceito: com letras a-f nao ha leitura decimal, mas tambem nao ha
	// como distinguir de um valor digitado errado — recusar e mais seguro que
	// adivinhar, e o operador ve o erro imediatamente.
	for _, v := range []string{"0x1F", "0X1F", "1fh", "1Fh", "0x1f"} {
		got, err := DecodeValue(TypeDWORD, v)
		if err != nil {
			t.Errorf("DecodeValue(DWORD, %q) erro: %v", v, err)
			continue
		}
		want := []byte{0x1F, 0x00, 0x00, 0x00}
		if !bytes.Equal(got, want) {
			t.Errorf("DecodeValue(DWORD, %q) = %v; queria %v", v, got, want)
		}
	}
}

// TestDecodeValue_RejectsInvalidBase64: binario invalido nao pode virar bytes
// parciais silenciosamente.
func TestDecodeValue_RejectsInvalidBase64(t *testing.T) {
	for _, v := range []string{"nao-e-base64!", "====", "a"} {
		if _, err := DecodeValue(TypeBinary, v); err == nil {
			t.Errorf("DecodeValue(BINARY, %q) deveria falhar", v)
		}
	}
}

// TestParseValueType cobre a normalizacao de tipos vindos do console.
func TestParseValueType(t *testing.T) {
	ok := map[string]ValueType{
		"REG_SZ": TypeString, "reg_sz": TypeString, "SZ": TypeString, "string": TypeString,
		"REG_EXPAND_SZ": TypeExpandString, "EXPAND_SZ": TypeExpandString,
		"REG_DWORD": TypeDWORD, "DWORD": TypeDWORD, "dword": TypeDWORD,
		"REG_QWORD": TypeQWORD, "QWORD": TypeQWORD,
		"REG_MULTI_SZ": TypeMultiString, "MULTI_SZ": TypeMultiString,
		"REG_BINARY": TypeBinary, "BINARY": TypeBinary,
		"REG_NONE": TypeNone,
		" DWORD ":  TypeDWORD,
	}
	for raw, want := range ok {
		got, err := ParseValueType(raw)
		if err != nil || got != want {
			t.Errorf("ParseValueType(%q) = (%v, %v); queria (%v, nil)", raw, got, err, want)
		}
	}
	// Desconhecidos NAO viram um default: escrever REG_SZ onde o operador quis
	// REG_DWORD corromperia o valor.
	for _, raw := range []string{"", "REG_INVENTADO", "boolean", "float"} {
		if _, err := ParseValueType(raw); err == nil {
			t.Errorf("ParseValueType(%q) deveria falhar em vez de adivinhar", raw)
		}
	}
}

// TestTypeFromID confirma o mapeamento dos IDs do header do Windows (winnt.h),
// que NAO correspondem a ordem das constantes do pacote x/sys.
func TestTypeFromID(t *testing.T) {
	expected := map[uint32]ValueType{
		0: TypeNone, 1: TypeString, 2: TypeExpandString, 3: TypeBinary,
		4: TypeDWORD, 5: TypeDWORDBigEndian, 6: TypeLink, 7: TypeMultiString,
		8: TypeResourceList, 9: TypeFullResourceDescriptor,
		10: TypeResourceRequirementsList, 11: TypeQWORD,
	}
	for id, want := range expected {
		got, ok := TypeFromID(id)
		if !ok || got != want {
			t.Errorf("TypeFromID(%d) = (%v, %v); queria (%v, true)", id, got, ok, want)
		}
	}
	if _, ok := TypeFromID(4242); ok {
		t.Error("TypeFromID de valor desconhecido deveria devolver ok=false")
	}
}

// TestValueType_IsEditable: tipos de estrutura binaria nao sao editaveis como
// texto — edita-los corrompe o formato interno.
func TestValueType_IsEditable(t *testing.T) {
	editable := []ValueType{TypeString, TypeExpandString, TypeDWORD, TypeQWORD, TypeMultiString, TypeBinary}
	for _, vt := range editable {
		if !vt.IsEditable() {
			t.Errorf("%s deveria ser editavel", vt)
		}
	}
	notEditable := []ValueType{TypeResourceList, TypeFullResourceDescriptor, TypeResourceRequirementsList, TypeLink, TypeNone}
	for _, vt := range notEditable {
		if vt.IsEditable() {
			t.Errorf("%s NAO deveria ser editavel (formato binario interno)", vt)
		}
	}
}

// TestUTF16_DecodeWithTrailingNUL garante que o terminador e descartado sem
// cortar NUL interno — um REG_SZ pode conter NUL no meio.
func TestUTF16_DecodeWithTrailingNUL(t *testing.T) {
	raw := encodeUTF16String("abc")
	if got := decodeUTF16String(raw); got != "abc" {
		t.Errorf("decodeUTF16String = %q; queria \"abc\"", got)
	}
	// Sem terminador tambem funciona.
	if got := decodeUTF16String([]byte{'a', 0, 'b', 0}); got != "ab" {
		t.Errorf("decodeUTF16String sem NUL final = %q; queria \"ab\"", got)
	}
	// Buffer vazio.
	if got := decodeUTF16String(nil); got != "" {
		t.Errorf("decodeUTF16String(nil) = %q; queria vazio", got)
	}
}

// TestUTF16_OddByteCount garante que um byte solto no fim e ignorado (nao ha
// como formar um par) em vez de inventar um 0x00 que mudaria o valor.
func TestUTF16_OddByteCount(t *testing.T) {
	raw := []byte{'a', 0, 'b'} // ultimo byte solto
	got := decodeUTF16String(raw)
	if got != "a" {
		t.Errorf("decodeUTF16String com contagem impar = %q; queria \"a\"", got)
	}
}

// TestMultiString_EmptyString e a representacao canonica de um MULTI_SZ vazio.
//
// Gravar 0 bytes faria o Windows tratar como REG_NONE; 00 00 00 00 e o correto
// (e o que o regedit produz).
func TestMultiString_EmptyString(t *testing.T) {
	raw := encodeUTF16MultiString(nil)
	want := []byte{0, 0, 0, 0}
	if !bytes.Equal(raw, want) {
		t.Errorf("MULTI_SZ vazio = %v; queria %v", raw, want)
	}
	got := decodeUTF16MultiString(raw)
	if len(got) != 0 {
		t.Errorf("decode de MULTI_SZ vazio = %v; queria lista vazia", got)
	}
}

// TestEncodeMultiStringFromList confirma o caminho da UI (lista -> Data).
//
// O formato e JSON, e NAO "\n"-separado: um item PODE conter newline literal (o
// registro permite), e nesse caso o separador simples divide um item em dois.
func TestEncodeMultiStringFromList(t *testing.T) {
	if got := EncodeMultiStringFromList([]string{"a", "b"}); got != `["a","b"]` {
		t.Errorf("EncodeMultiStringFromList = %q; queria [\"a\",\"b\"]", got)
	}
	if got := EncodeMultiStringFromList(nil); got != "[]" {
		t.Errorf("EncodeMultiStringFromList(nil) = %q; queria []", got)
	}
	if got := EncodeMultiStringFromList([]string{}); got != "[]" {
		t.Errorf("EncodeMultiStringFromList(vazio) = %q; queria []", got)
	}
}

// TestMultiString_NewlineInItemRoundTrip e o teste que justifica o formato JSON.
//
// Um item de MULTI_SZ pode conter newline literal. Com o separador "\n" esse
// item era silenciosamente dividido em dois — perda de dado sem erro visivel,
// que so apareceria quando o programa que le o valor quebrasse.
func TestMultiString_NewlineInItemRoundTrip(t *testing.T) {
	original := []string{"linha1\nlinha2", "segundo"}

	data := EncodeMultiStringFromList(original)
	raw, err := DecodeValue(TypeMultiString, data)
	if err != nil {
		t.Fatalf("DecodeValue: %v", err)
	}
	entry := EncodeValue("v", 7, raw)

	if len(entry.StringList) != len(original) {
		t.Fatalf("itens = %#v (%d); queria %d itens", entry.StringList, len(entry.StringList), len(original))
	}
	for i := range original {
		if entry.StringList[i] != original[i] {
			t.Errorf("item %d = %q; queria %q", i, entry.StringList[i], original[i])
		}
	}
}

// TestMultiString_AcceptsLegacyNewlineFormat garante compatibilidade: um viewer
// antigo que ainda envie "\n"-separado continua funcionando.
func TestMultiString_AcceptsLegacyNewlineFormat(t *testing.T) {
	raw, err := DecodeValue(TypeMultiString, "a\nb\nc")
	if err != nil {
		t.Fatalf("DecodeValue (formato legado): %v", err)
	}
	entry := EncodeValue("v", 7, raw)
	want := []string{"a", "b", "c"}
	if len(entry.StringList) != len(want) {
		t.Fatalf("itens = %#v; queria %#v", entry.StringList, want)
	}
	for i := range want {
		if entry.StringList[i] != want[i] {
			t.Errorf("item %d = %q; queria %q", i, entry.StringList[i], want[i])
		}
	}
}

// TestMultiString_InvalidJSONRejected: um array malformado nao pode virar
// silenciosamente uma lista com o texto cru dentro.
func TestMultiString_InvalidJSONRejected(t *testing.T) {
	if _, err := DecodeValue(TypeMultiString, "[nao-e-json"); err != ErrInvalidData {
		t.Errorf("err = %v; queria ErrInvalidData", err)
	}
}
