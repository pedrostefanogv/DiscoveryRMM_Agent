package app

import (
	"context"
	"sync"
	"time"
)

// companion_controller.go — máquina de estados do modo companion da UI
// (PLANO_SEPARACAO_SERVICO_UI.md §0.4 — item "companion.Controller testável").
// Extraída de startIPCClient (ipc_app_integration.go) para uma struct
// independente de Wails, com dependências injetadas e clock/ticker
// substituíveis — testável sem pipe real.
//
// Comportamento (M5 — SEM fallback standalone):
//   - Polling de status a cada PollInterval (5s): envia status ao serviço e
//     zera o contador de falhas quando a escrita funciona.
//   - Falha contínua por MaxFailures (60 × 5s = 5min) → OnServiceLost UMA VEZ
//     (estado "serviço ausente" na UI) e CONTINUA o polling — quando o
//     serviço voltar, o SendStatus volta a funcionar e o fluxo normal retoma.
//     A UI NUNCA assume core local (o core vive exclusivamente no serviço).
//   - Encerra com o contexto da App (shutdown limpo).

// CompanionTuner isola as ações que o Controller dispara — injetadas pela App.
type CompanionTuner interface {
	// SendStatus envia o snapshot de status ao serviço. Erro = serviço inacessível.
	SendStatus() error
	// OnServiceLost notifica a UI que o serviço sumiu (evento frontend).
	OnServiceLost(reason string)
	// OnServiceRecovered notifica a UI que o serviço voltou (M5: retoma estado).
	OnServiceRecovered()
}

// CompanionConfig parametriza o Controller (defaults em DefaultCompanionConfig).
type CompanionConfig struct {
	PollInterval time.Duration
	MaxFailures  int
	Reason       string
}

// DefaultCompanionConfig espelha os valores históricos do polling embutido.
func DefaultCompanionConfig() CompanionConfig {
	return CompanionConfig{
		PollInterval: 5 * time.Second,
		MaxFailures:  60, // 60 × 5s = 5 min
		Reason:       "service_unreachable",
	}
}

// CompanionController roda o polling de status do modo companion.
// Zero dependências de Wails/pipe: use FakeTuner em testes.
type CompanionController struct {
	tuner    CompanionTuner
	cfg      CompanionConfig
	mu       sync.Mutex
	failures int
	lost     bool // M5: evento de perda já notificado (não spamma a cada tick)
	running  bool
}

// NewCompanionController cria o controller com config customizada.
func NewCompanionController(tuner CompanionTuner, cfg CompanionConfig) *CompanionController {
	return &CompanionController{tuner: tuner, cfg: cfg}
}

// NewDefaultCompanionController cria o controller com os valores históricos.
func NewDefaultCompanionController(tuner CompanionTuner) *CompanionController {
	return NewCompanionController(tuner, DefaultCompanionConfig())
}

// Run executa o loop de polling até ctx.Done() ou stop. Bloqueante — rode em
// goroutine. Cada tick: envia status; N falhas seguidas ≥ MaxFailures notifica
// OnServiceLost (uma vez) e continua — M5: a UI espera o serviço voltar,
// sem nunca assumir o core.
func (c *CompanionController) Run(ctx context.Context, tickerC <-chan time.Time) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tickerC:
			if err := c.tuner.SendStatus(); err != nil {
				c.mu.Lock()
				c.failures++
				failed := c.failures
				lost := c.lost
				c.mu.Unlock()
				if failed >= c.cfg.MaxFailures {
					if !lost {
						c.mu.Lock()
						c.lost = true
						c.mu.Unlock()
						c.tuner.OnServiceLost(c.cfg.Reason)
					}
				}
				continue
			}
			c.mu.Lock()
			wasLost := c.lost
			c.failures = 0
			c.lost = false
			c.mu.Unlock()
			if wasLost {
				// Serviço voltou — a UI retoma o estado normal (o IPCClient
				// reconecta pelo próprio RunConnectLoop; aqui apenas o evento).
				c.tuner.OnServiceRecovered()
			}
		}
	}
}

// Failures expõe o contador atual (diagnóstico/testes).
func (c *CompanionController) Failures() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failures
}

// companionTunerAdapter adapta a App ao CompanionTuner (thin adapter).
type companionTunerAdapter struct{ a *App }

func (t companionTunerAdapter) SendStatus() error {
	return t.a.ipcClient.Send(NewIPCMessage(IPCMsgStatus, nil))
}
func (t companionTunerAdapter) OnServiceLost(reason string) {
	t.a.Logs.Append("[ipc] serviço ausente por 5min — modo interface aguardando serviço (M5: sem fallback standalone)")
	t.a.EmitEvent("service:companion_lost", map[string]any{"reason": reason})
}
func (t companionTunerAdapter) OnServiceRecovered() {
	t.a.Logs.Append("[ipc] serviço voltou — modo companion retomado")
	t.a.EmitEvent("service:companion_recovered", nil)
}