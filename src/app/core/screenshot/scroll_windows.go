//go:build windows

package screenshot

import (
	"fmt"
	"time"

	"discovery/app/core/screen"
)

// ScrollDown envia notches de roda do mouse para BAIXO no desktop ativo. A roda
// é o que faz o conteúdo rolar na janela em foco; usada pela captura de rolagem.
// O delta negativo corresponde a "para baixo" na convenção do Windows.
const (
	scrollWheelDownDelta  = -120
	scrollWheelMaxNotches = 10
)

func ScrollDown(notches int) error {
	if notches <= 0 {
		notches = 1
	}
	if notches > scrollWheelMaxNotches {
		notches = scrollWheelMaxNotches
	}
	for i := 0; i < notches; i++ {
		if err := screen.InjectMouseWheel(scrollWheelDownDelta); err != nil {
			return fmt.Errorf("rolagem do mouse falhou: %w", err)
		}
		// Pequeno intervalo entre notches: alguns apps coalescem eventos
		// consecutivos e perdem parte do deslocamento.
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}
