//go:build !windows

package notifyui

import "fmt"

// ShowDialog não é suportado fora do Windows (sem UI nativa do cartão).
func ShowDialog(title, text, buttons string) (string, error) {
	return "", fmt.Errorf("dialogo padronizado nao suportado neste SO")
}
