package windowsupdate

import (
	"context"
	"time"
)

// Options configura timeouts das operacoes WUA. O COM nao e cancelavel, por
// isso os limites sao aplicados por corrida (ver client_windows.go).
type Options struct {
	// ScanTimeout e o limite de um scan completo (pendentes + instaladas).
	ScanTimeout time.Duration
	// InstallTimeout e o limite de download+instalacao de um lote.
	InstallTimeout time.Duration
	// ShortTimeout cobre hide/unhide e leituras de politica.
	ShortTimeout time.Duration
}

// DefaultOptions devolve timeouts conservadores para uso no servico.
func DefaultOptions() Options {
	return Options{
		ScanTimeout:    10 * time.Minute,
		InstallTimeout: 60 * time.Minute,
		ShortTimeout:   2 * time.Minute,
	}
}

func (o Options) normalized() Options {
	def := DefaultOptions()
	if o.ScanTimeout <= 0 {
		o.ScanTimeout = def.ScanTimeout
	}
	if o.InstallTimeout <= 0 {
		o.InstallTimeout = def.InstallTimeout
	}
	if o.ShortTimeout <= 0 {
		o.ShortTimeout = def.ShortTimeout
	}
	return o
}

// Client abstrai a interacao com o Windows Update Agent.
type Client interface {
	// Probe reporta suporte/servico/politica sem rodar um scan.
	Probe(ctx context.Context) ProbeInfo
	// Scan lista updates pendentes (WUA) e instalados (QFE) + sonda de reboot.
	Scan(ctx context.Context, opts ScanOptions) (*ScanResult, error)
	// Install instala SOMENTE os targets que existirem no catalogo atual.
	Install(ctx context.Context, targets []InstallTarget) (*InstallResult, error)
	// Hide oculta/reexibe updates no WUA (enforcement no SO, opt-in).
	Hide(ctx context.Context, updateKeys []string, hidden bool) (int, error)
}

// New devolve o cliente WUA da plataforma atual.
func New(opts Options) Client {
	return newPlatformClient(opts.normalized())
}

// ProbePendingReboot e a sonda de reinicio pendente. Implementada por
// plataforma; em SO sem suporte retorna Pending=false.
func ProbePendingReboot() RebootInfo {
	return probePendingReboot()
}

// ReadPolicy le a politica de Windows Update efetiva (best-effort).
func ReadPolicy() (PolicySettings, error) {
	return readPolicy()
}

// ApplyPolicy aplica a politica e devolve o backup do estado anterior.
func ApplyPolicy(settings PolicySettings) (*PolicyBackup, error) {
	return applyPolicy(settings)
}

// RestorePolicy restaura o estado anterior capturado em backup.
func RestorePolicy(backup PolicyBackup) error {
	return restorePolicy(backup)
}
