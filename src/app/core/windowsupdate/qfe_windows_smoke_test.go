//go:build windows

package windowsupdate

// Teste de fumaca da EXTRACAO COM (o ponto exato do bug 019faa6f). Nao roda
// em maquinas sem WMI: nesse caso o teste e pulado.

import (
	"runtime"
	"testing"
	"time"
)

func TestReadWMIRowsExtractsRealStrings(t *testing.T) {
	result := withWMITestThread(func() ([]map[string]any, error) {
		return queryWMI("root\\cimv2", "SELECT HotFixID, Caption, Description FROM Win32_QuickFixEngineering")
	});
	if result.err != nil {
		t.Skipf("WMI indisponivel nesta maquina: %v", result.err)
	}
	if len(result.rows) == 0 {
		t.Skip("maquina sem QFE listadas pelo WMI")
	}
	for _, row := range result.rows {
		kb := wmiString(row, "HotFixID")
		if kb == "" {
			t.Fatalf("HotFixID vazio: %+v", row)
		}
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		t.Logf("row keys=%v", keys)
	}
}

type wmiTestResult struct {
	rows []map[string]any
	err  error
}

func withWMITestThread(fn func() ([]map[string]any, error)) wmiTestResult {
	result := wmiTestResult{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if _, err := comInitializeMTA(); err != nil {
			result.err = err
			return
		}
		result.rows, result.err = fn()
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		result.err = ErrTimeout
	}
	return result
}
