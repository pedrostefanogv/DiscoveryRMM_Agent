//go:build windows

package winregistry

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// ─────────────────────────────────────────────────────────────────────────────
// OPERACOES NO REGISTRO
//
// TODA funcao publica aqui chama normalizeAndCheck ANTES de tocar no registro.
// A ordem e o contrato: normalizar, avaliar o guard, e so entao abrir a chave.
// Um caminho nao normalizado avaliado pelo guard seria contornavel (ex.:
// "SECURITY\\." passaria por uma checagem de igualdade).
//
// As funcoes de mutacao checam \`confirmed\` como ULTIMA barreira do agente. A UI
// e a API ja exigem confirmacao; repetir aqui e defesa em profundidade — se um
// viewer adulterado publicar direto no NATS, a escrita ainda nao acontece.
// ─────────────────────────────────────────────────────────────────────────────

// pageSize limita quantas subchaves/valores uma resposta carrega.
//
// HKLM\SOFTWARE\Classes tem dezenas de milhares de subchaves; uma resposta
// unica estouraria o limite de mensagem do NATS (1 MB por padrao) e travaria o
// console. A paginacao e por TOKEN OPACO (offset numerico) para o cliente nao
// precisar entender o formato.
const pageSize = 500

// ListKey lista subchaves e valores de uma chave, com paginacao.
//
// token vazio = primeira pagina. O token e o offset em subchaves: valores sao
// sempre devolvidos por inteiro na pagina em que a chave aparece, porque uma
// chave com >500 valores e rarissima e paginar os dois eixos ao mesmo tempo
// complicaria o cliente sem ganho real.
func ListKey(hive Hive, keyPath, token string) (*ListPage, error) {
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpRead)
	if err != nil {
		return nil, err
	}

	key, user, err := openKeyWithUser(hive, normalized, false)
	if err != nil {
		return nil, err
	}
	defer key.Close()

	subkeys, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return nil, mapRegistryError(err, normalized)
	}
	// Ordena para a navegacao ser estavel entre chamadas (o SO nao garante
	// ordem) — sem isto, paginar devolveria itens repetidos/omitidos.
	sort.Strings(subkeys)

	offset := 0
	if strings.TrimSpace(token) != "" {
		parsed, perr := strconv.Atoi(strings.TrimSpace(token))
		if perr != nil || parsed < 0 {
			return nil, ErrInvalidPath
		}
		offset = parsed
	}
	if offset > len(subkeys) {
		offset = len(subkeys)
	}

	end := offset + pageSize
	hasMore := false
	if end >= len(subkeys) {
		end = len(subkeys)
	} else {
		hasMore = true
	}
	pageKeys := subkeys[offset:end]

	values := []ValueEntry{}
	if offset == 0 {
		values, err = readValues(key)
		if err != nil {
			return nil, err
		}
	}

	next := ""
	if hasMore {
		next = strconv.Itoa(end)
	}

	writable, denyReason := Writable(hive, normalized)

	return &ListPage{
		Hive:        string(hive),
		Path:        normalized,
		Subkeys:     pageKeys,
		Values:      values,
		HasMore:     hasMore,
		NextToken:   next,
		SubkeyCount: len(subkeys),
		ValueCount:  len(values),
		Writable:    &writable,
		DenyReason:  denyReason,
		ActiveUser:  user,
	}, nil
}

// readValues le todos os valores de uma chave ja aberta.
func readValues(key registry.Key) ([]ValueEntry, error) {
	names, err := key.ReadValueNames(-1)
	if err != nil {
		return nil, mapRegistryError(err, "")
	}
	sort.Strings(names)

	out := make([]ValueEntry, 0, len(names))
	for _, name := range names {
		data, typeID, verr := readRawValue(key, name)
		if verr != nil {
			// Um valor ilegivel nao derruba a listagem inteira (a chave continua
			// navegavel), mas precisa ser MARCADO: sem o campo ReadError o
			// operador veria "REG_NONE / vazio" e concluiria que o valor esta
			// limpo, quando na verdade a leitura falhou.
			out = append(out, ValueEntry{
				Name:      name,
				Type:      TypeNone,
				Data:      "",
				ReadError: verr.Error(),
			})
			continue
		}
		out = append(out, EncodeValue(name, typeID, data))
	}
	return out, nil
}

// StatKey devolve metadados de uma chave (para a arvore).
func StatKey(hive Hive, keyPath string) (*KeyInfo, error) {
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpRead)
	if err != nil {
		return nil, err
	}

	key, user, err := openKeyWithUser(hive, normalized, false)
	if err != nil {
		return nil, err
	}
	defer key.Close()

	subkeys, err := key.ReadSubKeyNames(-1)
	if err != nil {
		return nil, mapRegistryError(err, normalized)
	}
	values, err := key.ReadValueNames(-1)
	if err != nil {
		return nil, mapRegistryError(err, normalized)
	}

	writable, denyReason := Writable(hive, normalized)

	return &KeyInfo{
		Hive:        string(hive),
		Path:        normalized,
		SubkeyCount: len(subkeys),
		ValueCount:  len(values),
		Writable:    writable,
		DenyReason:  denyReason,
		ActiveUser:  user,
	}, nil
}

// GetValue le um valor especifico.
//
// Nome vazio e legitimo: e o valor "(Default)".
func GetValue(hive Hive, keyPath, name string) (*ValueEntry, error) {
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpRead)
	if err != nil {
		return nil, err
	}
	if verr := ValidateValueName(name); verr != nil {
		return nil, verr
	}

	key, err := openKey(hive, normalized, false)
	if err != nil {
		return nil, err
	}
	defer key.Close()

	data, typeID, err := readRawValue(key, name)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil, ErrValueNotFound
		}
		return nil, mapRegistryError(err, normalized)
	}

	entry := EncodeValue(name, typeID, data)
	return &entry, nil
}

// SetValue grava um valor (cria a chave se necessario).
func SetValue(hive Hive, keyPath, name string, vType ValueType, data string, confirmed bool) error {
	if !confirmed {
		return ErrConfirmationRequired
	}
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpWrite)
	if err != nil {
		return err
	}
	if verr := ValidateValueName(name); verr != nil {
		return verr
	}

	payload, err := DecodeValue(vType, data)
	if err != nil {
		return err
	}

	// Tipo precisa existir no mapa canonico antes de qualquer escrita: um tipo
	// desconhecido gravado como binario silenciosamente seria pior que o erro.
	if _, ok := registryTypeIDs[vType]; !ok {
		return ErrUnsupportedType
	}

	key, err := createKeyHandle(hive, normalized)
	if err != nil {
		return err
	}
	defer key.Close()

	// Os setters do pacote x/sys sao TIPADOS e usam as constantes DELE
	// (registry.SZ, DWORD, BINARY...), nao o ID cru do Windows. Ver
	// setTypedValue: passar o ID cru gravaria com o tipo errado — o classico
	// "o valor virou REG_BINARY depois de editar".
	if werr := setTypedValue(key, name, vType, payload); werr != nil {
		return mapRegistryError(werr, normalized)
	}
	return nil
}

// readRawValue le o tipo cru e os bytes de um valor.
//
// GetValue do pacote x/sys e de baixo nivel e usa duas chamadas: a primeira
// (buf nil) devolve apenas o TAMANHO e o tipo; a segunda le os bytes. Isso
// evita alocar um buffer de tamanho arbitrario — e, principalmente, garante que
// len(data) seja EXATAMENTE o do valor, sem bytes de folga no fim.
//
// E o ponto onde a licao do MeshAgent se aplica: derivar o tamanho do payload
// do tamanho da ALOCACAO (e nao do valor real) corrompe o dado gravado.
func readRawValue(key registry.Key, name string) ([]byte, uint32, error) {
	size, valType, err := key.GetValue(name, nil)
	if err != nil {
		return nil, 0, err
	}
	if size == 0 {
		// Valor de tamanho zero e legitimo (ex.: REG_SZ vazio, REG_BINARY
		// vazio). Nao chamamos GetValue de novo com buffer vazio porque o
		// pacote trataria como "consulta de tamanho" e entraria em loop.
		return []byte{}, valType, nil
	}
	buf := make([]byte, size)
	n, valType, err := key.GetValue(name, buf)
	if err != nil {
		return nil, 0, err
	}
	// Corta pelo n REAL devolvido, nao por len(buf): se o valor encolheu entre
	// as duas chamadas (corrida com outro escritor), len(buf) traria lixo.
	if n < len(buf) {
		buf = buf[:n]
	}
	return buf, valType, nil
}

// writeRawValue grava bytes crus com um tipo cru do Windows.
//
// Usado pela copia de subarvore (rename/export round-trip), onde o tipo veio do
// proprio SO. Os setters tipados do pacote nao aceitam o ID cru, entao o caminho
// e por syscall direta via registry.Key... que tambem nao expoe setValue
// publico. A saida e usar os setters tipados quando o tipo e conhecido e o
// caminho binario quando nao e.
func writeRawValue(key registry.Key, name string, valType uint32, data []byte) error {
	vType, known := TypeFromID(valType)
	if !known {
		// Tipo desconhecido (ou de recurso): grava com o ID CRU para NAO mudar o
		// tipo do valor. Usar SetBinaryValue aqui convertia um REG_RESOURCE_LIST
		// (8) em REG_BINARY (3) — corrupcao silenciosa de tipo numa copia/rename.
		if err := setValueRawType(key, name, valType, data); err == nil {
			return nil
		}
		return key.SetBinaryValue(name, data)
	}
	switch vType {
	case TypeString, TypeExpandString, TypeLink:
		return key.SetStringValue(name, decodeUTF16String(data))
	case TypeMultiString:
		return key.SetStringsValue(name, decodeUTF16MultiString(data))
	case TypeDWORD:
		if len(data) < 4 {
			return ErrInvalidData
		}
		return key.SetDWordValue(name, binary.LittleEndian.Uint32(data[:4]))
	case TypeQWORD:
		if len(data) < 8 {
			return ErrInvalidData
		}
		return key.SetQWordValue(name, binary.LittleEndian.Uint64(data[:8]))
	default:
		return key.SetBinaryValue(name, data)
	}
}

// setTypedValue grava respeitando o tipo declarado pelo operador.
//
// Recebe os BYTES ja decodificados por DecodeValue — que e a fonte da verdade do
// que sera gravado. Nao re-parseia a string original: fazer isso duplicava a
// logica de decodificacao e introduziu um bug real (o MULTI_SZ no formato JSON,
// que e um array, era dividido por quebra-de-linha e gravado como UM item
// literal com o texto do array dentro).
func setTypedValue(key registry.Key, name string, vType ValueType, payload []byte) error {
	switch vType {
	case TypeString:
		return key.SetStringValue(name, decodeUTF16String(payload))
	case TypeExpandString:
		return key.SetExpandStringValue(name, decodeUTF16String(payload))
	case TypeDWORD:
		if len(payload) < 4 {
			return ErrInvalidData
		}
		return key.SetDWordValue(name, binary.LittleEndian.Uint32(payload[:4]))
	case TypeQWORD:
		if len(payload) < 8 {
			return ErrInvalidData
		}
		return key.SetQWordValue(name, binary.LittleEndian.Uint64(payload[:8]))
	case TypeMultiString:
		// Os bytes ja sao o MULTI_SZ codificado (UTF-16LE com terminadores).
		// Gravar pelo tipo cru preserva exatamente o que DecodeValue produziu,
		// incluindo item vazio no meio e item com newline literal.
		return setValueRawType(key, name, registry.MULTI_SZ, payload)
	case TypeBinary:
		return key.SetBinaryValue(name, payload)
	default:
		// REG_NONE e tipos de recurso: grava os bytes crus PRESERVANDO o tipo.
		// Gravando como binario, o tipo do valor mudaria (ex.: REG_NONE -> 3).
		typeID, ok := registryTypeIDs[vType]
		if !ok {
			return ErrUnsupportedType
		}
		if err := setValueRawType(key, name, typeID, payload); err == nil {
			return nil
		}
		return key.SetBinaryValue(name, payload)
	}
}

// DeleteValue remove um valor.
func DeleteValue(hive Hive, keyPath, name string, confirmed bool) error {
	if !confirmed {
		return ErrConfirmationRequired
	}
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpDelete)
	if err != nil {
		return err
	}
	if verr := ValidateValueName(name); verr != nil {
		return verr
	}

	key, err := openKey(hive, normalized, true)
	if err != nil {
		return err
	}
	defer key.Close()

	if derr := key.DeleteValue(name); derr != nil {
		if derr == registry.ErrNotExist {
			return ErrValueNotFound
		}
		return mapRegistryError(derr, normalized)
	}
	return nil
}

// CreateKey cria uma chave (idempotente: se ja existir, nao e erro).
func CreateKey(hive Hive, keyPath string, confirmed bool) error {
	if !confirmed {
		return ErrConfirmationRequired
	}
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpWrite)
	if err != nil {
		return err
	}
	if normalized == "" {
		return ErrInvalidPath // nao se cria a raiz de um hive
	}

	key, err := createKeyHandle(hive, normalized)
	if err != nil {
		return err
	}
	return key.Close()
}

// DeleteKey remove uma chave.
//
// recursive=false exige que a chave esteja vazia (o SO recusa remover chave com
// subchaves). recursive=true apaga a subarvore inteira — por isso a UI precisa
// mostrar quantos itens serao removidos antes de confirmar.
func DeleteKey(hive Hive, keyPath string, recursive, confirmed bool) error {
	if !confirmed {
		return ErrConfirmationRequired
	}
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpDelete)
	if err != nil {
		return err
	}
	if normalized == "" {
		return ErrInvalidPath // nunca apagar a raiz de um hive
	}

	root, osPath, _, err := resolveTarget(hive, normalized)
	if err != nil {
		return err
	}

	if !recursive {
		// Nao-recursivo: a chave precisa estar VAZIA. Verificamos ANTES de
		// tentar apagar para devolver um erro acionavel em vez do erro cru do
		// SO. Sem isto o operador via "acesso negado" (o SO devolve
		// ERROR_ACCESS_DENIED para chave com filhos) e concluia que faltava
		// privilegio — quando o problema era a chave ter subchaves.
		key, oerr := registry.OpenKey(root, osPath, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE|registry.WOW64_64KEY)
		if oerr != nil {
			return mapRegistryError(oerr, normalized)
		}
		children, cerr := key.ReadSubKeyNames(-1)
		names, verr := key.ReadValueNames(-1)
		key.Close()
		if cerr != nil {
			return mapRegistryError(cerr, normalized)
		}
		if verr != nil {
			return mapRegistryError(verr, normalized)
		}
		if len(children) > 0 || len(names) > 0 {
			return fmt.Errorf("%w: a chave tem %d subchave(s) e %d valor(es); use a exclusao recursiva para remove-la",
				ErrKeyNotEmpty, len(children), len(names))
		}
		if derr := registry.DeleteKey(root, osPath); derr != nil {
			return mapRegistryError(derr, normalized)
		}
		return nil
	}

	// Recursivo: apaga de baixo para cima (o SO recusa remover chave com filhos).
	if derr := deleteTree(root, osPath); derr != nil {
		return mapRegistryError(derr, normalized)
	}
	return nil
}

// deleteTree remove uma chave e toda a sua subarvore.
//
// A ordem (filhos antes do pai) e obrigatoria: o SO recusa remover uma chave
// que ainda tem subchaves. A profundidade e limitada implicitamente pelo
// MaxPathLen validado no guard.
func deleteTree(root registry.Key, keyPath string) error {
	key, err := registry.OpenKey(root, keyPath, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		// Chave ja nao existe (corrida com outro tecnico): nada a fazer.
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	children, cerr := key.ReadSubKeyNames(-1)
	key.Close()
	if cerr != nil {
		return cerr
	}
	for _, child := range children {
		childPath := keyPath + "\\" + child
		if derr := deleteTree(root, childPath); derr != nil {
			return derr
		}
	}
	if derr := registry.DeleteKey(root, keyPath); derr != nil && derr != registry.ErrNotExist {
		return derr
	}
	return nil
}

// RenameKey renomeia uma chave (mesmo pai, novo nome).
//
// O registro do Windows NAO tem rename: a operacao e copiar a subarvore para o
// novo nome e remover a antiga. Isso significa que valores e a ACL sao perdidos
// na copia — por isso a UI avisa explicitamente antes de confirmar.
func RenameKey(hive Hive, keyPath, newName string, confirmed bool) error {
	if !confirmed {
		return ErrConfirmationRequired
	}
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpWrite)
	if err != nil {
		return err
	}
	if normalized == "" {
		return ErrInvalidPath
	}
	cleanNew, nerr := validateSingleComponent(newName)
	if nerr != nil {
		return nerr
	}

	// O destino tambem precisa passar pelo guard: renomear PARA dentro de uma
	// area protegida seria um contorno trivial se so a origem fosse checada.
	parent, _ := splitParent(normalized)
	targetPath := cleanNew
	if parent != "" {
		targetPath = parent + "\\" + cleanNew
	}
	if _, _, gerr := normalizeAndCheck(hive, targetPath, OpWrite); gerr != nil {
		return gerr
	}

	root, osSrc, user, err := resolveTarget(hive, normalized)
	if err != nil {
		return err
	}
	osDst := targetPath
	if user != nil && user.SID != "" {
		osDst = user.SID + "\\" + targetPath
	}

	// Ja existe destino? Recusar evita merge silencioso de duas chaves.
	if existing, oerr := registry.OpenKey(root, osDst, registry.QUERY_VALUE|registry.WOW64_64KEY); oerr == nil {
		existing.Close()
		return fmt.Errorf("%w: ja existe uma chave com o nome de destino", ErrInvalidPath)
	}

	source, oerr := registry.OpenKey(root, osSrc, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if oerr != nil {
		return mapRegistryError(oerr, normalized)
	}
	source.Close()

	if cerr := copyTree(root, osSrc, osDst); cerr != nil {
		return mapRegistryError(cerr, normalized)
	}
	if derr := deleteTree(root, osSrc); derr != nil {
		return mapRegistryError(derr, normalized)
	}
	return nil
}

// copyTree copia recursivamente valores e subchaves de src para dst.
func copyTree(root registry.Key, src, dst string) error {
	source, err := registry.OpenKey(root, src, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer source.Close()

	dest, _, err := registry.CreateKey(root, dst, registry.QUERY_VALUE|registry.SET_VALUE|registry.CREATE_SUB_KEY|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer dest.Close()

	// Valores.
	names, err := source.ReadValueNames(-1)
	if err != nil {
		return err
	}
	for _, name := range names {
		data, typeID, verr := readRawValue(source, name)
		if verr != nil {
			continue // valor ilegivel: nao aborta a copia inteira
		}
		if werr := writeRawValue(dest, name, typeID, data); werr != nil {
			return werr
		}
	}

	// Subchaves.
	children, err := source.ReadSubKeyNames(-1)
	if err != nil {
		return err
	}
	for _, child := range children {
		if cerr := copyTree(root, src+"\\"+child, dst+"\\"+child); cerr != nil {
			return cerr
		}
	}
	return nil
}

// validateSingleComponent valida um nome de chave (um unico componente).
func validateSingleComponent(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", ErrInvalidPath
	}
	if strings.ContainsAny(trimmed, "\\/") {
		return "", ErrInvalidPath
	}
	if trimmed == "." || trimmed == ".." {
		return "", ErrInvalidPath
	}
	if strings.ContainsRune(trimmed, 0) || hasControlChars(trimmed) {
		return "", ErrInvalidPath
	}
	return trimmed, nil
}

// splitParent separa o caminho do pai e o nome do ultimo componente.
func splitParent(keyPath string) (string, string) {
	idx := strings.LastIndex(keyPath, "\\")
	if idx < 0 {
		return "", keyPath
	}
	return keyPath[:idx], keyPath[idx+1:]
}

// ExportKey gera um .reg textual da chave.
//
// Aceita o formato que o regedit aceita (REGEDIT4/Windows Registry Editor
// Version 5.00). NAO executa reg.exe nem qualquer processo externo: a geracao e
// puramente em memoria, o que mantem o agente sem superficie de shell.
func ExportKey(hive Hive, keyPath string, recursive bool) (string, error) {
	normalized, _, err := normalizeAndCheck(hive, keyPath, OpRead)
	if err != nil {
		return "", err
	}

	root, osPath, _, err := resolveTarget(hive, normalized)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("Windows Registry Editor Version 5.00\r\n\r\n")
	if err := exportNode(&sb, root, hive, osPath, normalized, recursive, 0); err != nil {
		return "", err
	}

	if sb.Len() > maxExportBytes {
		return "", ErrLimitExceeded
	}
	return sb.String(), nil
}

// maxExportBytes limita o tamanho do .reg gerado.
//
// Exportar HKLM\SOFTWARE\Classes inteiro gera centenas de MB e derrubaria a
// sessao. O limite e explicitamente baixo; o operador exporta subarvores.
const maxExportBytes = 4 << 20 // 4 MB

// maxExportDepth limita a profundidade da exportacao recursiva.
const maxExportDepth = 32

func exportNode(sb *strings.Builder, root registry.Key, hive Hive, osPath, logicalPath string, recursive bool, depth int) error {
	if sb.Len() > maxExportBytes {
		return ErrLimitExceeded
	}
	if depth > maxExportDepth {
		return ErrLimitExceeded
	}

	key, err := registry.OpenKey(root, osPath, registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return mapRegistryError(err, logicalPath)
	}
	names, verr := key.ReadValueNames(-1)
	if verr != nil {
		key.Close()
		return mapRegistryError(verr, logicalPath)
	}
	children, cerr := key.ReadSubKeyNames(-1)
	if cerr != nil {
		key.Close()
		return mapRegistryError(cerr, logicalPath)
	}

	// O .reg usa o nome LONGO do hive (HKEY_LOCAL_MACHINE), nao a abreviacao.
	headerPath := longHiveName(hive)
	if logicalPath != "" {
		headerPath += "\\" + escapeRegPath(logicalPath)
	}
	sb.WriteString("[" + headerPath + "]\r\n")

	sort.Strings(names)
	for _, name := range names {
		data, typeID, rerr := readRawValue(key, name)
		if rerr != nil {
			continue
		}
		sb.WriteString(formatRegValue(name, typeID, data))
	}
	sb.WriteString("\r\n")
	key.Close()

	if !recursive {
		return nil
	}
	sort.Strings(children)
	for _, child := range children {
		childOSPath := child
		if osPath != "" {
			childOSPath = osPath + "\\" + child
		}
		childLogicalPath := child
		if logicalPath != "" {
			childLogicalPath = logicalPath + "\\" + child
		}
		if err := exportNode(sb, root, hive, childOSPath, childLogicalPath, recursive, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// longHiveName devolve o nome completo exigido pelo formato .reg.
func longHiveName(hive Hive) string {
	switch hive {
	case HiveLocalMachine:
		return "HKEY_LOCAL_MACHINE"
	case HiveCurrentUser:
		return "HKEY_CURRENT_USER"
	case HiveClassesRoot:
		return "HKEY_CLASSES_ROOT"
	case HiveUsers:
		return "HKEY_USERS"
	case HiveCurrentConfig:
		return "HKEY_CURRENT_CONFIG"
	default:
		return string(hive)
	}
}

// escapeRegPath escapa barras invertidas para o formato .reg.
//
// O .reg usa "\\" para representar uma barra invertida no nome. Sem isso, um
// nome de chave com barra literal geraria um arquivo invalido.
func escapeRegPath(p string) string {
	return strings.ReplaceAll(p, "\\", "\\\\")
}

// formatRegValue serializa um valor no formato .reg.
func formatRegValue(name string, typeID uint32, data []byte) string {
	vType, known := TypeFromID(typeID)
	keyName := escapeRegValueName(name)

	switch vType {
	case TypeString:
		return fmt.Sprintf("%q=%q\r\n", keyName, decodeUTF16String(data))
	case TypeExpandString:
		return fmt.Sprintf("%q=hex(2):%s\r\n", keyName, hexBytes(data))
	case TypeMultiString:
		return fmt.Sprintf("%q=hex(7):%s\r\n", keyName, hexBytes(data))
	case TypeDWORD:
		if len(data) >= 4 {
			return fmt.Sprintf("%q=dword:%08x\r\n", keyName, binary32(data))
		}
	case TypeQWORD:
		return fmt.Sprintf("%q=hex(b):%s\r\n", keyName, hexBytes(data))
	}
	if !known {
		return fmt.Sprintf("%q=hex:%s\r\n", keyName, hexBytes(data))
	}
	return fmt.Sprintf("%q=hex:%s\r\n", keyName, hexBytes(data))
}

// escapeRegValueName escapa aspas e barras no nome de um valor.
func escapeRegValueName(name string) string {
	if name == "" {
		return "@" // o valor (Default) e escrito como "@"
	}
	return strings.ReplaceAll(strings.ReplaceAll(name, "\\", "\\\\"), "\"", "\\\"")
}

// hexBytes formata bytes como "aa,bb,cc," (formato do .reg, com virgula final).
func hexBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	parts := make([]string, 0, len(data))
	for _, b := range data {
		parts = append(parts, fmt.Sprintf("%02x", b))
	}
	return strings.Join(parts, ",") + ","
}

func binary32(data []byte) uint32 {
	return uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16 | uint32(data[3])<<24
}

// Base64Encode e exposto para o protocolo (usado nos testes e pelo chamador).
func Base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
