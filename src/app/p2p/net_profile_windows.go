//go:build windows

package p2p

import (
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

var isPublicNetworkProfileFn = isPublicNetworkProfile

// isPublicNetworkProfile consulta o Windows Network List Manager (COM
// INetworkListManager) e retorna true apenas quando há pelo menos uma rede
// conectada no perfil Público (categoria 0) e NENHUMA em perfil Privado (1) ou
// Domínio (2). Em caso de erro COM, retorna false (fail-open para não quebrar
// descoberta LAN).
func isPublicNetworkProfile() (bool, string) {
	type result struct {
		isPublic bool
		reason   string
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		if err := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); err != nil {
			// S_FALSE / RPC_E_CHANGED_MODE não devem impedir uso, mas se falhar
			// totalmente seguimos fail-open.
			ch <- result{}
			return
		}
		defer ole.CoUninitialize()

		clsid, err := ole.CLSIDFromString("{DCB00C01-570F-4A9B-8D69-199FDBA5723B}")
		if err != nil {
			ch <- result{}
			return
		}
		unk, err := ole.CreateInstance(clsid, ole.IID_IUnknown)
		if err != nil || unk == nil {
			ch <- result{}
			return
		}
		defer unk.Release()

		disp, err := unk.QueryInterface(ole.IID_IDispatch)
		if err != nil || disp == nil {
			ch <- result{}
			return
		}
		defer disp.Release()

		// NLM_ENUM_NETWORK_CONNECTED = 1
		netsVar, err := oleutil.CallMethod(disp, "GetNetworks", 1)
		if err != nil || netsVar == nil {
			ch <- result{}
			return
		}
		defer netsVar.Clear()

		netsDisp := netsVar.ToIDispatch()
		if netsDisp == nil {
			ch <- result{}
			return
		}

		var hasPublic, hasTrusted bool
		_ = oleutil.ForEach(netsDisp, func(v *ole.VARIANT) error {
			netDisp := v.ToIDispatch()
			if netDisp == nil {
				return nil
			}
			catVar, catErr := oleutil.CallMethod(netDisp, "GetCategory")
			if catErr != nil || catVar == nil {
				return nil
			}
			cat := catVar.Val
			catVar.Clear()
			switch cat {
			case 0: // NLM_NETWORK_CATEGORY_PUBLIC
				hasPublic = true
			case 1, 2: // NLM_NETWORK_CATEGORY_PRIVATE, NLM_NETWORK_CATEGORY_DOMAIN_AUTHENTICATED
				hasTrusted = true
			}
			return nil
		})

		if hasPublic && !hasTrusted {
			ch <- result{
				isPublic: true,
				reason:   "perfil de rede Publica ativo sem rede Privada/Dominio",
			}
			return
		}
		ch <- result{}
	}()

	res := <-ch
	return res.isPublic, res.reason
}
