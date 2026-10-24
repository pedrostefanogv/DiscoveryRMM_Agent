//go:build !windows

package notifyui

// ShowToast não é suportado fora do Windows.
func ShowToast(req ToastRequest) error {
	return toastUnsupported{}
}

type toastUnsupported struct{}

func (toastUnsupported) Error() string { return "toast suportado apenas no Windows" }
