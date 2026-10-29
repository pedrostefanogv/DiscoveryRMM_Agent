//go:build windows

package windowsupdate

import (
	"fmt"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// queryInstalledQFE lista as KBs instaladas via Win32_QuickFixEngineering
// (WMI/COM). E MUITO mais barato que IUpdateSearcher.Search com IsInstalled=1,
// que devolve o catalogo inteiro.
//
// Pre-requisito: COM inicializado como MTA no thread atual (o chamador e
// scanWUA, que roda dentro de withOp).
func queryInstalledQFE() ([]Update, error) {
	rows, err := queryWMI("root\\cimv2", "SELECT HotFixID, Description, Caption, InstalledOn FROM Win32_QuickFixEngineering")
	if err != nil {
		return nil, err
	}
	return buildsInstalledUpdatesFromRows(rows), nil
}

// buildsInstalledUpdatesFromRows converte as linhas do WMI em Updates
// instalados (KBs unicas, titulo normalizado). Separado do COM para permitir
// teste com rows sinteticas.
func buildsInstalledUpdatesFromRows(rows []map[string]any) []Update {
	updates := make([]Update, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		kb := NormalizeKB(wmiString(row, "HotFixID"))
		if kb == "" {
			continue
		}
		key := KBKey(kb)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		title := NormalizeQFETitle(kb, wmiString(row, "Caption"), wmiString(row, "Description"))
		update := Update{
			UpdateKey:   key,
			KBIDs:       []string{kb},
			KBID:        kb,
			Title:       title.Title,
			Description: title.Description,
			IsInstalled: true,
			Source:      SourceQFE,
		}
		if installedAt := parseWMIInstalledOn(wmiString(row, "InstalledOn")); installedAt != nil {
			update.InstalledAt = installedAt
		}
		updates = append(updates, update)
	}
	return updates
}

// parseWMIInstalledOn aceita "20240115", "2024-01-15", "1/15/2024",
// "2026-10-16 21:09:57", datetime completo do CIM e RFC3339. O WMI em pt-BR
// costuma devolver "16/10/2026". Retorna nil quando nao parseavel.
func parseWMIInstalledOn(raw string) *time.Time {
	return parseWMIInstalledOnAt(raw, time.Now())
}

// parseWMIInstalledOnAt e parseWMIInstalledOn com "agora" injetavel (teste).
func parseWMIInstalledOnAt(raw string, now time.Time) *time.Time {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	// CIM datetime (20261016210957.000000-180) tem sufixo de fuso numerico;
	// normaliza para UTC sem o sufixo.
	if len(value) >= 14 && value[4] == '-' && value[7] == '-' {
		value = strings.Replace(value, " ", "T", 1)
	}
	// Layouts NAO ambiguos: o separador/ordem identifica dia, mes e ano.
	// CIM datetime normalizado para "AAAA-MM-DD HH:MM:SS". O layout do Go usa
	// "2006-01-02 15:04:05" como exemplo canonico.
	layouts := []string{
		"20060102",
		"2006-01-02",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}

	// Datas com barra: "05/03/2026" casa nos DOIS layouts (dd/mm e mm/dd) e o
	// resultado depende do locale. Quem decide é o formato curto de data do
	// próprio Windows (LOCALE_SSHORTDATE): heurística temporal (escolher a mais
	// recente no passado) trocava dia/mês em pt-BR, que é justamente onde o WMI
	// devolve "16/10/2026".
	candidates := make([]installedOnCandidate, 0, 2)
	for _, layout := range []struct {
		format   string
		dayFirst bool
	}{
		{format: "02/01/2006 15:04:05", dayFirst: true},
		{format: "01/02/2006 15:04:05", dayFirst: false},
		{format: "02/01/2006", dayFirst: true},
		{format: "01/02/2006", dayFirst: false},
	} {
		if parsed, err := time.Parse(layout.format, value); err == nil {
			candidates = append(candidates, installedOnCandidate{at: parsed.UTC(), dayFirst: layout.dayFirst})
		}
	}
	if chosen := chooseInstalledOn(candidates, now); chosen != nil {
		return chosen
	}

	// CIM "[...]" pode vir com microssegundos; tenta o prefixo de 14 digitos.
	if len(value) >= 8 {
		digits := value[:8]
		if parsed, err := time.Parse("20060102", digits); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

// installedOnCandidate e uma interpretacao possivel de uma data com barra.
type installedOnCandidate struct {
	at       time.Time
	dayFirst bool
}

// windowsShortDateDayFirst informa se o formato curto de data do SO é
// dia-primeiro (pt-BR: dd/MM/yyyy) ou mes-primeiro (en-US: M/d/yyyy).
// Variavel para teste: em produção le LOCALE_SSHORTDATE via GetLocaleInfoEx.
var windowsShortDateDayFirst = detectShortDateDayFirst

// chooseInstalledOn escolhe entre as interpretacoes ambiguas de uma data:
//  1. usa o formato curto de data do SO (locale) como desempate principal;
//  2. nunca aceita data no FUTURO (instalacao futura é invalida) — se a
//     interpretacao do locale cair no futuro, tenta a outra;
//  3. locale desconhecido: heurística antiga (a mais próxima de agora).
func chooseInstalledOn(candidates []installedOnCandidate, now time.Time) *time.Time {
	if len(candidates) == 0 {
		return nil
	}
	now = now.UTC()
	if len(candidates) == 1 {
		// Uma única interpretação possível: aceita, exceto se estiver no FUTURO
		// (relógio errado ou data corrompida) — melhor sem data do que com uma
		// data impossível, ainda mais agora que a recoleta sobrescreve.
		if candidates[0].at.After(now) {
			return nil
		}
		chosen := candidates[0].at
		return &chosen
	}

	notFuture := func(candidate installedOnCandidate) bool { return !candidate.at.After(now) }

	if dayFirst, known := windowsShortDateDayFirst(); known {
		for _, candidate := range candidates {
			if candidate.dayFirst == dayFirst && notFuture(candidate) {
				chosen := candidate.at
				return &chosen
			}
		}
		// Interpretacao do locale no futuro: fica a outra que nao esteja.
		for _, candidate := range candidates {
			if candidate.dayFirst != dayFirst && notFuture(candidate) {
				chosen := candidate.at
				return &chosen
			}
		}
	}

	// Locale desconhecido (ou ambas no futuro): mais proxima de agora no
	// passado; se todas no futuro, a menos distante.
	var best *time.Time
	for _, candidate := range candidates {
		if candidate.at.After(now) {
			continue
		}
		if best == nil || candidate.at.After(*best) {
			chosen := candidate.at
			best = &chosen
		}
	}
	if best != nil {
		return best
	}
	for _, candidate := range candidates {
		if best == nil || candidate.at.Before(*best) {
			chosen := candidate.at
			best = &chosen
		}
	}
	return best
}

// detectShortDateDayFirst le LOCALE_SSHORTDATE do SO. known=false quando o
// Windows nao responde (chamador cai na heurística temporal).
func detectShortDateDayFirst() (bool, bool) {
	const localeShortDate = 0x1F // LOCALE_SSHORTDATE

	buffer := make([]uint16, 128)
	length, _, _ := procGetLocaleInfoEx.Call(
		0, // NULL = LOCALE_NAME_USER_DEFAULT (servico: locale do sistema)
		uintptr(localeShortDate),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
	)
	if length == 0 {
		return false, false
	}
	format := syscall.UTF16ToString(buffer[:length-1])
	dayIndex := strings.IndexAny(format, "d")
	monthIndex := strings.Index(format, "M")
	if dayIndex < 0 || monthIndex < 0 {
		return false, false
	}
	return dayIndex < monthIndex, true
}

var procGetLocaleInfoEx = func() *syscall.LazyProc {
	dll := syscall.NewLazyDLL("kernel32.dll")
	return dll.NewProc("GetLocaleInfoEx")
}()

// queryWMI executa uma consulta WQL via COM. Valores sao normalizados em
// string/int64/float64/bool.
func queryWMI(namespace, query string) ([]map[string]any, error) {
	unknown, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return nil, fmt.Errorf("CreateObject SWbemLocator: %w", err)
	}
	defer unknown.Release()

	wmi, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, fmt.Errorf("QueryInterface: %w", err)
	}
	defer wmi.Release()

	serviceRaw, err := oleutil.CallMethod(wmi, "ConnectServer", nil, namespace)
	if err != nil {
		return nil, fmt.Errorf("ConnectServer: %w", err)
	}
	service := serviceRaw.ToIDispatch()
	if service == nil {
		serviceRaw.Clear()
		return nil, fmt.Errorf("ConnectServer retornou nil")
	}
	defer service.Release()

	queryRaw, err := oleutil.CallMethod(service, "ExecQuery", query)
	if err != nil {
		return nil, fmt.Errorf("ExecQuery: %w", err)
	}
	queryObj := queryRaw.ToIDispatch()
	if queryObj == nil {
		queryRaw.Clear()
		return nil, fmt.Errorf("ExecQuery retornou nil")
	}
	defer queryObj.Release()

	rows, err := readWMIRows(queryObj)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// readWMIRows enumera uma SWbemObjectSet e devolve as propriedades de cada
// objeto.
//
// BUG CORRIGIDO (maquina 019faa6f): a leitura anterior usava
// `nameRaw.ToString()`/`normalizeWMIValue` sobre VARIANTs EMPRESTADOS pelo
// ForEach e o ToString do go-ole chama SysStringLen, que depende do cabecalho
// do BSTR. Com o VARIANT aposentado/realocado a leitura devolvia 0 e TODA
// string virava "" (titulos "Atualizacao KB5127839", descricao vazia), mesmo o
// WMI tendo Caption preenchido. Agora:
//   - o NOME da propriedade vem do BSTR ponteiro direto (VARIANT_BSTRString);
//   - o VALOR e lido por uma copia em stack (Invoke), cuja VARIANT e de nossa
//     propriedade e liberada com Clear() — o BSTR e convertido ANTES do Clear.
func readWMIRows(queryObj *ole.IDispatch) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, 32)
	err := oleutil.ForEach(queryObj, func(itemVar *ole.VARIANT) error {
		item := itemVar.ToIDispatch()
		if item == nil {
			return nil
		}
		defer item.Release()

		propsRaw, err := oleutil.GetProperty(item, "Properties_")
		if err != nil {
			return nil
		}
		props := propsRaw.ToIDispatch()
		if props == nil {
			propsRaw.Clear()
			return nil
		}
		// propsRaw.ToIDispatch() transfere a referencia: Release libera.
		defer props.Release()

		row := make(map[string]any, 12)
		_ = oleutil.ForEach(props, func(propVar *ole.VARIANT) error {
			prop := propVar.ToIDispatch()
			if prop == nil {
				return nil
			}
			defer prop.Release()

			name := wmiPropertyName(prop)
			value, ok := readWMIPropertyValue(prop)
			if name == "" || !ok {
				return nil
			}
			row[name] = value
			return nil
		})
		if len(row) == 0 {
			return nil
		}
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// wmiPropertyName le SWbemProperty.Name usando copia do BSTR. Retorna "" em
// caso de erro.
func wmiPropertyName(prop *ole.IDispatch) string {
	raw, err := oleutil.GetProperty(prop, "Name")
	if err != nil || raw == nil {
		return ""
	}
	name := variantBSTRString(raw)
	_ = raw.Clear()
	return strings.TrimSpace(name)
}

// readWMIPropertyValue le SWbemProperty.Value e normaliza. Ok=false quando o
// WMI nao devolve valor (a propriedade e omitida da linha).
func readWMIPropertyValue(prop *ole.IDispatch) (any, bool) {
	raw, err := oleutil.GetProperty(prop, "Value")
	if err != nil || raw == nil {
		return nil, false
	}
	// Importante: converter ANTES do Clear (o Clear libera o BSTR).
	value := normalizeWMIValue(raw)
	_ = raw.Clear()
	return value, true
}

// variantBSTRString converte uma VARIANT BSTR em string lendo os UTF-16 ate o
// terminador NUL a partir do ponteiro. Nao usa SysStringLen (que depende do
// cabecalho do BSTR prefixado e falha quando a VARIANT aponta para memoria
// reaproveitada); o BSTR e sempre terminado em NUL por contrato do COM.
func variantBSTRString(v *ole.VARIANT) string {
	if v == nil {
		return ""
	}
	if v.VT&ole.VT_BYREF != 0 {
		// VT_BSTR|VT_BYREF: nao ocorre no fluxo WMI usado aqui; recusado de
		// proposito (a conversao de endereco em uintptr nao e verificavel pelo
		// vet de unsafe e nao ha necessidade de suporta-la).
		return ""
	}
	if v.VT != ole.VT_BSTR {
		return ""
	}
	ptr := *(**uint16)(unsafe.Pointer(&v.Val))
	if ptr == nil {
		return ""
	}
	return bstrPointerToString(ptr)
}

// bstrPointerToString le uma string UTF-16 terminada em NUL com limite de
// seguranca (32K caracteres) para nao ler memoria arbitraria sem fim.
func bstrPointerToString(ptr *uint16) string {
	if ptr == nil {
		return ""
	}
	const maxLength = 32768
	buf := unsafe.Slice(ptr, maxLength)
	units := make([]uint16, 0, 64)
	for _, unit := range buf {
		if unit == 0 {
			break
		}
		units = append(units, unit)
	}
	if len(units) == 0 {
		return ""
	}
	return string(utf16.Decode(units))
}

func normalizeWMIValue(v *ole.VARIANT) any {
	if v == nil {
		return ""
	}
	switch v.VT {
	case ole.VT_BSTR:
		return variantBSTRString(v)
	case ole.VT_I4, ole.VT_I2, ole.VT_I1, ole.VT_UI4, ole.VT_UI2, ole.VT_UI1:
		return int64(v.Val)
	case ole.VT_R4, ole.VT_R8:
		return v.Value()
	case ole.VT_BOOL:
		if v.Val != 0 {
			return int64(1)
		}
		return int64(0)
	case ole.VT_DATE:
		if value, ok := v.Value().(time.Time); ok {
			return value.Format(time.RFC3339)
		}
		return ""
	case ole.VT_NULL, ole.VT_EMPTY:
		return ""
	default:
		if v.VT&ole.VT_ARRAY != 0 {
			return ""
		}
		if value := v.Value(); value != nil {
			switch typed := value.(type) {
			case string:
				return typed
			case *ole.IDispatch, *ole.IUnknown:
				return ""
			default:
				return fmt.Sprint(typed)
			}
		}
		return variantBSTRString(v)
	}
}

func wmiString(row map[string]any, key string) string {
	if row == nil {
		return ""
	}
	// Nomes de propriedade do WMI sao case-insensitive na pratica; normalizamos
	// a procura para nao depender do casing devolvido pelo provedor.
	value, ok := row[key]
	if !ok {
		for name, candidate := range row {
			if strings.EqualFold(name, key) {
				value, ok = candidate, true
				break
			}
		}
	}
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case float64:
		return fmt.Sprintf("%.0f", typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}
