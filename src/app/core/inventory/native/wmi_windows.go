//go:build windows

package native

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// HRESULTs relevantes à inicialização do COM.
const (
	hresultSFalse             uintptr = 0x00000001 // COM já inicializado neste thread
	hresultRPC_E_CHANGED_MODE uintptr = 0x80010106 // thread já inicializado em outro apartment
)

// comInitializeMTA inicializa o COM como MTA no thread atual.
//
//	RPC_E_CHANGED_MODE NÃO é erro fatal: o thread já foi inicializado em
//	outro apartment (STA — webview2/Wails, go-toast, etc. no mesmo processo)
//	e o COM é usável do mesmo jeito; nesse caso NÃO chamamos CoUninitialize
//	(desbalancearia a inicialização de outra biblioteca).
//
// Antes, qualquer erro que não fosse "S_FALSE" textual abortava wmiQuery —
// e TODAS as consultas WMI do agente falhavam silenciosamente (memória,
// GPU, CPUInfo, motherboard/BIOS, fabricante…), pois os coletores usam
// `if rows, err := wmiQuery(...); err == nil`.
func comInitializeMTA() (needsUninit bool, err error) {
	err = ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED)
	if err == nil {
		return true, nil
	}
	var oleErr *ole.OleError
	if errors.As(err, &oleErr) {
		switch oleErr.Code() {
		case hresultSFalse:
			// Refcount incrementado — deve balancear com CoUninitialize.
			return true, nil
		case hresultRPC_E_CHANGED_MODE:
			return false, nil
		}
	}
	return false, err
}

// wmiQuery executes a WQL query against the given WMI namespace and returns
// the rows as a slice of maps (property name -> value). It uses COM via
// go-ole, avoiding any PowerShell subprocess.
//
// The returned values are normalized: strings become string, numbers become
// int64/float64, and nil becomes "".
func wmiQuery(namespace, query string) ([]map[string]any, error) {
	// Ver comInitializeMTA: S_OK/S_FALSE exigem CoUninitialize;
	// RPC_E_CHANGED_MODE usa o COM já ativo do thread sem CoUninitialize.
	needsUninit, err := comInitializeMTA()
	if err != nil {
		return nil, fmt.Errorf("CoInitializeEx: %w", err)
	}
	if needsUninit {
		defer ole.CoUninitialize()
	}

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

	// ConnectServer(server, namespace, user, password, locale, flags, authority, context)
	serviceRaw, err := oleutil.CallMethod(wmi, "ConnectServer", nil, namespace)
	if err != nil {
		return nil, fmt.Errorf("ConnectServer: %w", err)
	}
	service := serviceRaw.ToIDispatch()
	if service == nil {
		return nil, fmt.Errorf("ConnectServer returned nil")
	}
	defer service.Release()

	// ExecQuery(query)
	queryRaw, err := oleutil.CallMethod(service, "ExecQuery", query)
	if err != nil {
		return nil, fmt.Errorf("ExecQuery: %w", err)
	}
	queryObj := queryRaw.ToIDispatch()
	if queryObj == nil {
		return nil, fmt.Errorf("ExecQuery returned nil")
	}
	defer queryObj.Release()

	// Enumerate the collection.
	countRaw, err := oleutil.GetProperty(queryObj, "Count")
	if err != nil {
		return nil, fmt.Errorf("GetProperty Count: %w", err)
	}
	count := int(countRaw.Val)

	rows := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.CallMethod(queryObj, "ItemIndex", i)
		if err != nil {
			continue
		}
		item := itemRaw.ToIDispatch()
		if item == nil {
			continue
		}

		propsRaw, err := oleutil.GetProperty(item, "Properties_")
		if err != nil {
			item.Release()
			continue
		}
		props := propsRaw.ToIDispatch()
		if props == nil {
			item.Release()
			continue
		}

		// IMPORTANTE: SWbemPropertySet NÃO suporta ItemIndex (só Item por
		// nome). Antes o CallMethod("ItemIndex", j) falhava silenciosamente
		// (continue) e TODA linha saía com 0 propriedades — memória, GPU,
		// CPUInfo, motherboard/BIOS, impressoras etc. sempre vazias.
		// oleutil.ForEach resolve o _NewEnum/IEnumVARIANT internamente.
		row := make(map[string]any, 16)
		enumErr := oleutil.ForEach(props, func(propVar *ole.VARIANT) error {
			prop := propVar.ToIDispatch()
			if prop == nil {
				return nil
			}
			nameRaw, nameErr := oleutil.GetProperty(prop, "Name")
			valRaw, valErr := oleutil.GetProperty(prop, "Value")
			if nameErr == nil && valErr == nil {
				name := nameRaw.ToString()
				row[name] = normalizeWMIValue(valRaw)
			}
			if nameRaw != nil {
				nameRaw.Clear()
			}
			if valRaw != nil {
				valRaw.Clear()
			}
			prop.Release()
			return nil
		})
		if enumErr != nil {
			props.Release()
			item.Release()
			continue
		}

		props.Release()
		item.Release()
		rows = append(rows, row)
	}

	return rows, nil
}

// normalizeWMIValue converts an OLE VARIANT value into a Go-friendly value.
func normalizeWMIValue(v *ole.VARIANT) any {
	if v == nil {
		return ""
	}
	switch v.VT {
	case ole.VT_BSTR:
		return v.ToString()
	case ole.VT_I4, ole.VT_I2, ole.VT_I1, ole.VT_UI4, ole.VT_UI2, ole.VT_UI1:
		return int64(v.Val)
	case ole.VT_I8, ole.VT_UI8:
		// Valores 64-bit (ex.: Win32_Processor.NumberOfCores em alguns
		// sistemas) — go-ole expõe I8/UI8 como int64/uint64 em v.Val?
		// (depende da versão). Usamos o valor convertido via int64.
		if v.VT == ole.VT_UI8 {
			return int64(v.Val) // 32-bit truncation warning — ver nota abaixo
		}
		return v.Value()
	case ole.VT_R4, ole.VT_R8:
		return v.Value()
	case ole.VT_BOOL:
		if v.Val != 0 {
			return int64(1)
		}
		return int64(0)
	case ole.VT_NULL, ole.VT_EMPTY:
		return ""
	default:
		// Try to stringify.
		return v.ToString()
	}
}

// wmiString returns the string value of a WMI row field.
func wmiString(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		return fmt.Sprintf("%.0f", t)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", t))
	}
}

// wmiInt returns the int value of a WMI row field.
func wmiInt(row map[string]any, key string) int {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n := 0
		fmt.Sscanf(t, "%d", &n)
		return n
	default:
		return 0
	}
}

// wmiFloat returns the float64 value of a WMI row field.
func wmiFloat(row map[string]any, key string) float64 {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case int64:
		return float64(t)
	case float64:
		return t
	case string:
		f := 0.0
		fmt.Sscanf(t, "%f", &f)
		return f
	default:
		return 0
	}
}

// utf16Ptr is a small helper to build a *uint16 from a string.
func utf16Ptr(s string) *uint16 {
	return syscall.StringToUTF16Ptr(s)
}

// unsafePtr is a helper to convert a pointer to uintptr for syscall calls.
func unsafePtr(p unsafe.Pointer) uintptr {
	return uintptr(p)
}
