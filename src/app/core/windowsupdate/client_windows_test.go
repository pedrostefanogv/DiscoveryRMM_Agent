//go:build windows

package windowsupdate

// Leitura das propriedades inteiras do WUA via late binding. MaxDownloadSize e
// IUpdate.MaxDownloadSize (bytes) e chega como VT_I4/VT_UI4/VT_I8: precisa ser
// lido sem truncar (tamanho acima de 2 GiB nao pode virar negativo) e sem
// derrubar o update quando a propriedade falha.

import (
	"testing"

	ole "github.com/go-ole/go-ole"
)

func TestVariantInt64ReadsMaxDownloadSizeTypes(t *testing.T) {
	cases := []struct {
		name string
		v    *ole.VARIANT
		want int64
	}{
		{name: "VT_I4", v: &ole.VARIANT{VT: ole.VT_I4, Val: 1_234_567}, want: 1_234_567},
		{name: "VT_UI4 acima de 2GiB", v: &ole.VARIANT{VT: ole.VT_UI4, Val: 4_000_000_000}, want: 4_000_000_000},
		{name: "VT_I8", v: &ole.VARIANT{VT: ole.VT_I8, Val: 5_000_000_000}, want: 5_000_000_000},
		{name: "VT_UI2", v: &ole.VARIANT{VT: ole.VT_UI2, Val: 4096}, want: 4096},
		{name: "nulo", v: nil, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := variantInt64(tc.v); got != tc.want {
				t.Fatalf("variantInt64=%d want %d", got, tc.want)
			}
		})
	}
}
