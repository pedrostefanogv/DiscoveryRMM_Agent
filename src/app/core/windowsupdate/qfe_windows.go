//go:build windows

package windowsupdate

import (
	"fmt"
	"strings"
	"time"

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

		title := strings.TrimSpace(wmiString(row, "Caption"))
		if title == "" {
			title = "Atualizacao " + kb
		}
		update := Update{
			UpdateKey:   key,
			KBIDs:       []string{kb},
			KBID:        kb,
			Title:       truncate(title, 500),
			Description: truncate(strings.TrimSpace(wmiString(row, "Description")), 2000),
			IsInstalled: true,
			Source:      SourceQFE,
		}
		if installedAt := parseWMIInstalledOn(wmiString(row, "InstalledOn")); installedAt != nil {
			update.InstalledAt = installedAt
		}
		updates = append(updates, update)
	}
	return updates, nil
}

// parseWMIInstalledOn aceita "20240115", "2024-01-15", "1/15/2024" e datetime
// completo do CIM. Retorna nil quando nao parseavel.
func parseWMIInstalledOn(raw string) *time.Time {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	layouts := []string{
		"20060102",
		"2006-01-02",
		"01/02/2006",
		"20060102150405.000000-420",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	// Fallback: o SWbemProperty pode entregar time.Time direto.
	return nil
}

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
// objeto. Usa oleutil.ForEach porque SWbemPropertySet nao suporta ItemIndex.
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
		defer props.Release()

		row := make(map[string]any, 12)
		_ = oleutil.ForEach(props, func(propVar *ole.VARIANT) error {
			prop := propVar.ToIDispatch()
			if prop == nil {
				return nil
			}
			defer prop.Release()

			nameRaw, nameErr := oleutil.GetProperty(prop, "Name")
			valueRaw, valueErr := oleutil.GetProperty(prop, "Value")
			if nameErr == nil && valueErr == nil {
				row[nameRaw.ToString()] = normalizeWMIValue(valueRaw)
			}
			if nameRaw != nil {
				nameRaw.Clear()
			}
			if valueRaw != nil {
				valueRaw.Clear()
			}
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

func normalizeWMIValue(v *ole.VARIANT) any {
	if v == nil {
		return ""
	}
	switch v.VT {
	case ole.VT_BSTR:
		return v.ToString()
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
		if value := v.Value(); value != nil {
			return value
		}
		return v.ToString()
	}
}

func wmiString(row map[string]any, key string) string {
	value, ok := row[key]
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
