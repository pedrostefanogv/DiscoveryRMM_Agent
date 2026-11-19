package screen

import (
	"sync"
	"testing"
	"time"

	"discovery/app/core/winui"
)

// TestSnapshotMascarasConcorrencia prova que o snapshot publicado é imutável:
// dois consumidores simultâneos + um mutador que troca versão/pixels. Era a
// corrida da versão intermediária do cache (reaproveitava o array via [:0]+append
// enquanto outro loop de captura ainda iterava o snapshot anterior). Com -race,
// esta corrida é detectada se alguém voltar a escrever em array publicado.
func TestSnapshotMascarasConcorrencia(t *testing.T) {
	limparMascaras(t)
	mascaraSnapMu.Lock()
	mascaraSnapInit = false
	mascaraSnapCache = nil
	mascaraSnapMu.Unlock()
	t.Cleanup(func() {
		mascaraSnapMu.Lock()
		mascaraSnapInit = false
		mascaraSnapCache = nil
		mascaraSnapMu.Unlock()
	})

	m := mascaraDeTeste(0xBEEF, winui.Rect{X: 0, Y: 0, W: 64, H: 64}, 0x11, true)
	mascarasMu.Lock()
	mascaras[m.hwnd] = m
	mascarasMu.Unlock()
	mascaraVisivel = func(uintptr) bool { return true }

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Consumidor 1 e 2: leem o snapshot em loop (como dois loops de captura).
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				lista := snapshotMascaras()
				for j := range lista {
					_ = lista[j].hwnd
					_ = lista[j].rect
					_ = len(lista[j].px)
				}
			}
		}()
	}

	// Mutador: troca o buffer de pixels com copy-on-write (como o refresh).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			select {
			case <-stop:
				return
			default:
			}
			novo := make([]byte, len(m.px))
			novo[0] = byte(i)
			mascarasMu.Lock()
			m.px = novo
			m.versao++
			mascarasMu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()
}
