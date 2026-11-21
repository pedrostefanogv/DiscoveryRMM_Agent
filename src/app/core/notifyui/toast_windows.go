//go:build windows

package notifyui

import (
	"strings"
	"sync"

	toast "git.sr.ht/~jackmordaunt/go-toast/v2"
)

// appDataOnce garante que o AppID global seja registrado no registro do
// Windows uma única vez por processo (exigido pelo Windows Runtime para exibir
// o nome/ícone do agente no toast e no Action Center).
var (
	appDataOnce sync.Once
	appDataErr  error
)

func neutralizeToastInterpolation(value string) string {
	if !strings.ContainsAny(value, "$`") {
		return value
	}
	return strings.NewReplacer("$", "$\u200b", "`", "`\u200b").Replace(value)
}

// ShowToast envia um toast do Windows. Não é bloqueante além do envio: o
// Windows gerencia exibição/encerramento.
func ShowToast(req ToastRequest) error {
	req.AppID = strings.TrimSpace(req.AppID)
	if req.AppID == "" {
		req.AppID = DefaultToastAppID
	}
	appDataOnce.Do(func() {
		appData := toast.AppData{AppID: req.AppID}
		if req.IconPath != "" {
			appData.IconPath = req.IconPath
		}
		appDataErr = toast.SetAppData(appData)
	})
	n := toast.Notification{
		AppID: req.AppID,
		Title: neutralizeToastInterpolation(req.Title),
		Body:  neutralizeToastInterpolation(req.Message),
		Icon:  req.IconPath,
		Audio: toast.Default,
	}
	if req.Silent {
		n.Audio = toast.Silent
	}
	return n.Push()
}
