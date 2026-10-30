//go:build windows

package screen

import (
	"fmt"
	"unsafe"

	"github.com/kirides/go-d3d/d3d11"
	"github.com/kirides/go-d3d/dxgi"
)

// ── Sonda determinística do formato da duplicação DXGI ───────────────────────
//
// A ORDEM DOS CANAIS (R↔B) dos frames do go-d3d depende de qual caminho a
// dependência conseguiu abrir:
//
//	IDXGIOutput5::DuplicateOutput1(R8G8B8A8_UNORM) OK
//	    → staging herda R8G8B8A8 → Snapshot entrega RGBA → precisa swap R↔B
//	DuplicateOutput1 recusado → fallback IDXGIOutput1::DuplicateOutput
//	    → entrega o formato NATIVO do desktop (B8G8R8A8) → NÃO precisa swap
//
// A dependência guarda essa decisão num campo privado (needsSwizzle), então até
// aqui o projeto "adivinhava" o padrão e só corrigia depois da calibração por
// GDI — foi a causa dos ~68 s de cores invertidas na sessão de 2026-10-29
// (DESKTOP-DH608V7): o palpite era RGBA e o caminho real era o fallback BGRA.
//
// Esta sonda reproduz a MESMA decisão (mesmo device, mesmo adaptador, mesmo
// output, mesmo formato) e devolve a resposta em milissegundos. A duplicação de
// teste é liberada na hora; a sonda roda DEPOIS de a duplicação real existir,
// então uma falha aqui nunca derruba a captura — só devolve "desconhecida" e a
// calibração por GDI volta a decidir (comportamento antigo).
const (
	hresultOK                      = 0
	hresultEInvalidArg             = 0x80070057
	dxgiErrorInvalidCall           = 0x887A0001
	dxgiErrorUnsupported           = 0x887A0004
	dxgiErrorNotFound              = 0x887A0002
	dxgiErrorNotCurrentlyAvailable = 0x887A0022
)

// detectForcedDuplicateFormat responde se o go-d3d vai conseguir forçar
// DXGI_FORMAT_R8G8B8A8_UNORM neste device/output.
//
// Retorna (ChannelOrderRGBA, nil) quando o formato forçado é aceito,
// (ChannelOrderBGRA, nil) quando o driver recusa o formato e o fallback
// nativo (BGRA) será usado, e (ChannelOrderUnknown, erro) quando não é possível
// concluir — nesse caso a calibração por GDI decide.
func detectForcedDuplicateFormat(device *d3d11.ID3D11Device, monitorIndex int) (ChannelOrder, error) {
	if device == nil {
		return ChannelOrderUnknown, fmt.Errorf("device D3D11 nulo")
	}

	// Espelha outputduplication.newIDXGIOutputDuplicationFormat: pega o adapter
	// do DEVICE (não o adapter 0 da factory) e enumera o output pedido.
	var dxgiDevice1 *dxgi.IDXGIDevice1
	if hr := device.QueryInterface(dxgi.IID_IDXGIDevice1, &dxgiDevice1); hr != 0 || dxgiDevice1 == nil {
		return ChannelOrderUnknown, fmt.Errorf("QI IDXGIDevice1: HRESULT 0x%X", uint32(hr))
	}
	defer dxgiDevice1.Release()

	var pAdapter unsafe.Pointer
	if hr := dxgiDevice1.GetParent(dxgi.IID_IDXGIAdapter1, &pAdapter); hr != 0 || pAdapter == nil {
		return ChannelOrderUnknown, fmt.Errorf("GetParent(IDXGIAdapter1): HRESULT 0x%X", uint32(hr))
	}
	adapter := (*dxgi.IDXGIAdapter1)(pAdapter)
	defer adapter.Release()

	var output *dxgi.IDXGIOutput
	if hr := adapter.EnumOutputs(uint32(monitorIndex), &output); hr != 0 || output == nil {
		return ChannelOrderUnknown, fmt.Errorf("EnumOutputs(%d): HRESULT 0x%X", monitorIndex, hr)
	}
	defer output.Release()

	var output5 *dxgi.IDXGIOutput5
	if hr := output.QueryInterface(dxgi.IID_IDXGIOutput5, &output5); hr != 0 || output5 == nil {
		// Sem IDXGIOutput5 o go-d3d nem cria o capturer (ele exige a interface),
		// então este caminho é raro e não decide nada.
		return ChannelOrderUnknown, fmt.Errorf("QI IDXGIOutput5: HRESULT 0x%X", uint32(hr))
	}
	defer output5.Release()

	var dup *dxgi.IDXGIOutputDuplication
	hr := output5.DuplicateOutput1(dxgiDevice1, 0, []dxgi.DXGI_FORMAT{dxgi.DXGI_FORMAT_R8G8B8A8_UNORM}, &dup)
	if hr == hresultOK && dup != nil {
		dup.Release()
		return ChannelOrderRGBA, nil
	}
	if dup != nil {
		// Comportamento defensivo: se a chamada falhou mas devolveu objeto,
		// libera para não vazar referência COM.
		dup.Release()
	}

	switch uint32(hr) {
	case hresultEInvalidArg, dxgiErrorUnsupported, dxgiErrorInvalidCall:
		// Recusa explícita do formato → o go-d3d vai usar DuplicateOutput e
		// receber o formato nativo do desktop (BGRA no Windows 10 SDR).
		return ChannelOrderBGRA, nil
	case dxgiErrorNotCurrentlyAvailable, dxgiErrorNotFound:
		// Output em uso por outra duplicação (cenário de swap de capturer com a
		// anterior ainda aberta) ou output inexistente: não é uma resposta
		// sobre formato — não decide.
		return ChannelOrderUnknown, fmt.Errorf("DuplicateOutput1 inconclusivo: HRESULT 0x%X", uint32(hr))
	}
	return ChannelOrderUnknown, fmt.Errorf("DuplicateOutput1: HRESULT 0x%X", uint32(hr))
}
