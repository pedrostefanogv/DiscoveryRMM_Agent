package app

import (
	"sync"
	"testing"
)

// BUG (revisão): markServerAuthorizationPending não guardava o estado de
// "servidor inalcançável" sob o mesmo mutex usado na leitura — a corrida abaixo
// aparece no -race. Também: um caminho de rede definia unreachable fora do lock.
func TestServerAuthorizationFlagsAreRaceFree(t *testing.T) {
	a := &App{}
	body := []byte(`{"error":"Agent registration is pending (zero-touch)."}`)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				a.noteServerAuthorizationRefusal(approvalResponse(), body, nil)
				return
			}
			_, _, _ = a.serverAuthorizationPending()
			_ = a.IsServerAuthorizationPending()
			_ = a.GetOnboardingStatus()
		}(i)
	}
	wg.Wait()
}
