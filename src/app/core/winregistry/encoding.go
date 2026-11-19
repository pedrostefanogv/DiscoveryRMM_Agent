package winregistry

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ─────────────────────────────────────────────────────────────────────────────
// CODEC DE VALORES
//
// Este arquivo e a fronteira onde editores de registro costumam corromper
// dados. Duas regras aprendidas de analise de agentes RMM reais:
//
//  1. NUNCA derive o tamanho de um payload do tamanho de uma ALOCACAO. Use
//     sempre len(data) real. Um buffer maior que o conteudo gera bytes de lixo
//     no fim do valor — foi um bug real e corrigido na causa-raiz no MeshAgent.
//
//  2. Tipos binarios viajam em BASE64, nunca como string "crua". REG_BINARY
//     contem bytes que nao sao UTF-8 valido por definicao; converte-los para
//     string perde bytes de forma silenciosa e irreversivel.
//
// Nada aqui toca o registro: sao funcoes puras de conversao, testaveis isoladas.
// ─────────────────────────────────────────────────────────────────────────────

// registryTypeIDs mapeia o tipo canonico para o valor que o header do Windows
// usa. Os numeros sao os da API (winnt.h) e NAO correspondem a ordem do enum
// golang.org/x/sys/windows/registry — por isso o mapa explicito.
var registryTypeIDs = map[ValueType]uint32{
	TypeNone:                     0,
	TypeString:                   1,
	TypeExpandString:             2,
	TypeBinary:                   3,
	TypeDWORD:                    4,
	TypeDWORDBigEndian:           5,
	TypeLink:                     6,
	TypeMultiString:              7,
	TypeResourceList:             8,
	TypeFullResourceDescriptor:   9,
	TypeResourceRequirementsList: 10,
	TypeQWORD:                    11,
}

// typeIDsRegistry reverte registryTypeIDs para decodificar o que veio do SO.
var typeIDsRegistry = func() map[uint32]ValueType {
	m := make(map[uint32]ValueType, len(registryTypeIDs))
	for t, id := range registryTypeIDs {
		m[id] = t
	}
	return m
}()

// ParseValueType normaliza o tipo recebido do console.
//
// Aceita a grafia canonica e os aliases usuais (lowercase, sem o prefixo REG_,
// "dword", "sz"...). Recusa o que nao conhece — nao "chuta" um tipo default,
// porque escrever REG_SZ onde o operador quis REG_DWORD corrompe o valor.
func ParseValueType(raw string) (ValueType, error) {
	key := strings.ToUpper(strings.TrimSpace(raw))
	if key == "" {
		return "", ErrUnsupportedType
	}
	// Aliases curtos.
	switch key {
	case "SZ", "STRING", "REG_STRING":
		return TypeString, nil
	case "EXPAND_SZ", "EXPANDSTRING", "REG_EXPAND_STRING":
		return TypeExpandString, nil
	case "DWORD", "REG_DWORD_LE", "INT", "INT32":
		return TypeDWORD, nil
	case "QWORD", "INT64":
		return TypeQWORD, nil
	case "MULTI_SZ", "MULTISTRING", "REG_MULTI_STRING":
		return TypeMultiString, nil
	case "BINARY", "BIN", "REG_BINARY_DATA":
		return TypeBinary, nil
	case "NONE":
		return TypeNone, nil
	}
	// Grafia canonica (ou com o prefixo REG_ ja presente).
	if !strings.HasPrefix(key, "REG_") {
		key = "REG_" + key
	}
	for t := range registryTypeIDs {
		if string(t) == key {
			return t, nil
		}
	}
	return "", ErrUnsupportedType
}

// TypeFromID converte o ID cru do Windows para o tipo canonico.
//
// Um ID desconhecido devolve ok=false: o chamador decide (enviamos como
// REG_NONE com o dado em base64, em vez de inventar um tipo).
func TypeFromID(id uint32) (ValueType, bool) {
	t, ok := typeIDsRegistry[id]
	return t, ok
}

// IsEditable informa se a UI deve permitir editar o dado deste tipo.
//
// Tipos de recurso (RESOURCE_LIST, FULL_RESOURCE_DESCRIPTOR,
// RESOURCE_REQUIREMENTS_LIST) e REG_LINK sao estruturas binarias com formato
// interno proprio: editar como texto as corrompe. Ficam somente-leitura na UI
// (o dado continua visivel em base64).
func (t ValueType) IsEditable() bool {
	switch t {
	case TypeString, TypeExpandString, TypeDWORD, TypeQWORD, TypeMultiString, TypeBinary:
		return true
	default:
		return false
	}
}

// EncodeValue converte o dado bruto lido do registro para a representacao do
// protocolo (sempre string).
//
// Retorna o ValueEntry preenchido e o dado DECODIFICADO para reenvio opcional.
func EncodeValue(name string, typeID uint32, data []byte) ValueEntry {
	vType, known := TypeFromID(typeID)
	if !known {
		// Tipo desconhecido: preserva os bytes em base64 e marca como REG_NONE.
		// Perder o dado seria pior que perde-lo; inventar um tipo, pior ainda.
		return ValueEntry{
			Name: name,
			Type: TypeNone,
			Data: base64.StdEncoding.EncodeToString(data),
		}
	}

	entry := ValueEntry{Name: name, Type: vType}

	switch vType {
	case TypeString, TypeExpandString, TypeLink:
		// REG_SZ e NUL-terminado. len(data) real, nunca o tamanho do buffer.
		entry.Data = decodeUTF16String(data)

	case TypeMultiString:
		list := decodeUTF16MultiString(data)
		entry.StringList = list
		// Data carrega a forma JSON (sem perda), que e o que a UI devolve ao
		// gravar. O "\n"-separado NAO serve aqui: um item com newline literal
		// seria dividido em dois na volta. StringList continua sendo o canal
		// conveniente para a UI, e Data o canal exato para o round-trip.
		if encoded, jerr := json.Marshal(list); jerr == nil {
			entry.Data = string(encoded)
		} else {
			entry.Data = strings.Join(list, "\n")
		}

	case TypeDWORD:
		if len(data) >= 4 {
			entry.Data = strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data[:4])), 10)
		} else {
			entry.Data = base64.StdEncoding.EncodeToString(data)
		}

	case TypeDWORDBigEndian:
		if len(data) >= 4 {
			entry.Data = strconv.FormatUint(uint64(binary.BigEndian.Uint32(data[:4])), 10)
		} else {
			entry.Data = base64.StdEncoding.EncodeToString(data)
		}

	case TypeQWORD:
		if len(data) >= 8 {
			// FormatUint (e nao FormatInt): QWORD e sem sinal no registro. Um
			// valor >= 2^63 viraria negativo se tratado como int64 — o mesmo
			// cuidado que o JavaScript do console exige, ja que o protocolo
			// carrega isto como STRING.
			entry.Data = strconv.FormatUint(binary.LittleEndian.Uint64(data[:8]), 10)
		} else {
			entry.Data = base64.StdEncoding.EncodeToString(data)
		}

	default:
		// REG_BINARY, REG_NONE e tipos de recurso: base64, byte-exato.
		entry.Data = base64.StdEncoding.EncodeToString(data)
	}

	return entry
}

// DecodeValue converte o dado do protocolo (string) para os bytes que serao
// gravados no registro.
//
// E o inverso de EncodeValue e o ponto onde um editor ingenuo destroi dados.
func DecodeValue(vType ValueType, data string) ([]byte, error) {
	switch vType {
	case TypeString, TypeExpandString, TypeLink:
		return encodeUTF16String(data), nil

	case TypeMultiString:
		// Duas formas aceitas, nesta ordem de precedencia:
		//
		//  1. JSON array (["a","b"]) — formato SEM PERDA, usado pela UI atual.
		//  2. "\n"-separado — formato legado, mantido para compatibilidade com
		//     um viewer que ainda nao tenha sido atualizado.
		//
		// Por que o JSON e o formato canonico: um item de MULTI_SZ PODE conter
		// newline literal (o registro nao proibe). Com o separador "\n", esse
		// item era silenciosamente DIVIDIDO em dois — perda de dado sem erro,
		// que so apareceria quando o programa que le o valor quebrasse. O JSON
		// escapa o newline e o round-trip fica exato.
		if data == "" {
			return encodeUTF16MultiString(nil), nil
		}
		if strings.HasPrefix(strings.TrimSpace(data), "[") {
			var items []string
			if err := json.Unmarshal([]byte(data), &items); err == nil {
				return encodeUTF16MultiString(items), nil
			}
			return nil, ErrInvalidData
		}
		parts := strings.Split(data, "\n")
		return encodeUTF16MultiString(parts), nil

	case TypeDWORD:
		n, err := parseUintFlexible(data, 32)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, uint32(n))
		return buf, nil

	case TypeDWORDBigEndian:
		n, err := parseUintFlexible(data, 32)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, 4)
		binary.BigEndian.PutUint32(buf, uint32(n))
		return buf, nil

	case TypeQWORD:
		n, err := parseUintFlexible(data, 64)
		if err != nil {
			return nil, err
		}
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, n)
		return buf, nil

	case TypeBinary, TypeNone:
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
		if err != nil {
			return nil, ErrInvalidData
		}
		return decoded, nil

	default:
		return nil, ErrUnsupportedType
	}
}

// EncodeMultiStringFromList monta o Data (forma JSON) a partir da lista da UI.
//
// Existe separado porque o canal canonico para MULTI_SZ e StringList. Usa JSON
// e nao "\n" porque um item PODE conter newline literal (o registro permite), e
// nesse caso o separador simples divide um item em dois — perda silenciosa de
// dado. O JSON escapa o newline e preserva a lista exata.
func EncodeMultiStringFromList(list []string) string {
	if list == nil {
		return "[]"
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// parseUintFlexible aceita decimal e hexadecimal ("0x1F", "1f h").
//
// bits limita o maximo (32 ou 64). Recusa negativo: o registro trata DWORD e
// QWORD como SEM SINAL. "-1" e o erro mais comum de quem vem de linguagem com
// inteiro assinado, e aceita-lo silenciosamente gravaria 0xFFFFFFFF quando o
// operador talvez quisesse outra coisa — melhor recusar e explicar.
func parseUintFlexible(raw string, bits int) (uint64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, ErrInvalidData
	}
	if strings.HasPrefix(s, "-") {
		return 0, ErrInvalidData
	}

	base := 10
	body := s
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "0x"):
		base = 16
		body = s[2:]
	case strings.HasSuffix(lower, "h") && len(s) > 1:
		// Sufixo "h" da notacao hexadecimal do assembly/regedit.
		base = 16
		body = s[:len(s)-1]
	}
	if body == "" {
		return 0, ErrInvalidData
	}

	n, err := strconv.ParseUint(body, base, bits)
	if err != nil {
		return 0, ErrInvalidData
	}
	return n, nil
}

// ── Helpers UTF-16LE ────────────────────────────────────────────────────────

// decodeUTF16String converte bytes UTF-16LE NUL-terminados em string Go.
//
// Usa len(data) REAL e descarta um ultimo NUL 0x0000 (o terminador), mas
// preserva NULs internos se existirem — um valor REG_SZ pode conter NUL no
// meio, e cortar ali truncaria o valor em silencio.
func decodeUTF16String(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	// Remove o terminador final (par 00 00), se presente.
	if len(data) >= 2 && data[len(data)-1] == 0 && data[len(data)-2] == 0 {
		data = data[:len(data)-2]
	}
	return string(utf16.Decode(bytesToUTF16(data)))
}

// decodeUTF16MultiString converte bytes UTF-16LE de REG_MULTI_SZ em []string.
//
// Formato: sequencia de strings NUL-terminadas, encerrada por um NUL extra.
// String VAZIA no meio e significativa e e PRESERVADA; so o terminador final e
// descartado. A distincao exige cortar o terminador pelo FIM da sequencia, e nao
// parar no primeiro par de NULs consecutivos — ver o comentario no corpo.
func decodeUTF16MultiString(data []byte) []string {
	if len(data) == 0 {
		return []string{}
	}
	units := bytesToUTF16(data)
	// MULTI_SZ vazio (00 00 00 00): uma string vazia + terminador. Devolvemos
	// lista VAZIA, e nao [""], porque e assim que o regedit apresenta — um item
	// fantasma na UI sugeriria um valor que nao existe no registro.
	if len(units) == 2 && units[0] == 0 && units[1] == 0 {
		return []string{}
	}
	if len(units) == 1 && units[0] == 0 {
		return []string{}
	}

	// O terminador final e o ULTIMO NUL da sequencia, nao o primeiro par de NULs
	// consecutivos. Isto importa: ["a","","b"] contem dois NULs seguidos no
	// MEIO (a string vazia do meio), e trata-los como fim da lista perderia o
	// "b" — o bug classico de split ingenuo. Por isso o corte e feito do FIM
	// para o comeco, removendo apenas o terminador final.
	if len(units) >= 1 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	// E o NUL que terminava a ultima string real.
	if len(units) >= 1 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}

	// Agora cada NUL restante e um separador REAL entre strings.
	out := []string{}
	var cur []uint16
	for _, u := range units {
		if u == 0 {
			out = append(out, string(utf16.Decode(cur)))
			cur = nil
			continue
		}
		cur = append(cur, u)
	}
	out = append(out, string(utf16.Decode(cur)))
	return out
}

// encodeUTF16String gera bytes UTF-16LE com terminador NUL.
func encodeUTF16String(s string) []byte {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 0, (len(units)+1)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	buf = append(buf, 0, 0)
	return buf
}

// encodeUTF16MultiString gera REG_MULTI_SZ.
//
// Formato: cada string terminada por NUL, e um NUL EXTRA ao final da lista.
// Lista vazia produz 00 00 00 00 (uma string vazia + terminador), que e o que o
// regedit grava.
func encodeUTF16MultiString(list []string) []byte {
	// MULTI_SZ vazio tem representacao canonica de 4 bytes (00 00 00 00): uma
	// string vazia mais o terminador. Sem este caso explicito o laco abaixo nao
	// executaria e so o terminador final seria escrito (2 bytes), o que o
	// Windows nao le como MULTI_SZ — o valor apareceria como REG_NONE no
	// regedit em vez de uma lista vazia.
	if len(list) == 0 {
		return []byte{0, 0, 0, 0}
	}

	buf := make([]byte, 0, 8)
	for _, s := range list {
		units := utf16.Encode([]rune(s))
		for _, u := range units {
			buf = append(buf, byte(u), byte(u>>8))
		}
		// Terminador de CADA string.
		buf = append(buf, 0, 0)
	}
	// Terminador final da lista (NUL extra).
	buf = append(buf, 0, 0)
	return buf
}

// bytesToUTF16 converte []byte UTF-16LE em []uint16.
//
// Usa len(data) real: se o numero de bytes for impar, o ultimo byte solto e
// ignorado (nao ha como forma um par). Ignorar e o correto — inventar um byte
// 0x00 mudaria o valor.
func bytesToUTF16(data []byte) []uint16 {
	n := len(data) / 2
	units := make([]uint16, n)
	for i := 0; i < n; i++ {
		units[i] = uint16(data[i*2]) | uint16(data[i*2+1])<<8
	}
	return units
}
