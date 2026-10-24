//go:build !windows

package notifyui

// ShowMessageBox não é suportado fora do Windows.
func ShowMessageBox(title, text, buttons string) (string, error) {
	return "", messageboxUnsupported{}
}

type messageboxUnsupported struct{}

func (messageboxUnsupported) Error() string { return "MessageBox suportada apenas no Windows" }
