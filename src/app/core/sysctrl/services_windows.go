//go:build windows

package sysctrl

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// ServiceInfo representa um serviço Windows.
type ServiceInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	State       string `json:"state"`     // running, stopped, paused, ...
	StartType   string `json:"startType"` // auto, demand, disabled, ...
	BinaryPath  string `json:"binaryPath,omitempty"`
	PID         uint32 `json:"pid,omitempty"`
}

func stateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start_pending"
	case svc.StopPending:
		return "stop_pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue_pending"
	case svc.PausePending:
		return "pause_pending"
	case svc.Paused:
		return "paused"
	default:
		return "unknown"
	}
}

func startTypeName(t uint32) string {
	switch t {
	case mgr.StartAutomatic:
		return "auto"
	case mgr.StartManual:
		return "demand"
	case mgr.StartDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// ListServices enumera todos os serviços (ativos e parados) no Windows.
func ListServices() ([]ServiceInfo, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("mgr.Connect: %w", err)
	}
	defer m.Disconnect()

	names, err := m.ListServices()
	if err != nil {
		return nil, fmt.Errorf("ListServices: %w", err)
	}

	services := make([]ServiceInfo, 0, len(names))
	for _, name := range names {
		s, err := m.OpenService(name)
		if err != nil {
			// Serviço pode sumir entre listar e abrir — ignora.
			continue
		}
		cfg, cfgErr := s.Config()
		status, _ := s.Query()
		si := ServiceInfo{
			Name:        name,
			DisplayName: name,
			State:       stateName(status.State),
			PID:         status.ProcessId,
		}
		if cfgErr == nil {
			si.DisplayName = cfg.DisplayName
			si.BinaryPath = cfg.BinaryPathName
			si.StartType = startTypeName(cfg.StartType)
		}
		s.Close()
		services = append(services, si)
	}
	return services, nil
}

// StartService inicia um serviço pelo nome.
func StartService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("mgr.Connect: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("OpenService: %w", err)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("Start(%s): %w", name, err)
	}
	return nil
}

// StopService para um serviço pelo nome (envia SERVICE_CONTROL_STOP).
func StopService(name string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("mgr.Connect: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("OpenService: %w", err)
	}
	defer s.Close()

	_, err = s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("Stop(%s): %w", name, err)
	}
	return nil
}

// RestartService reinicia um serviço (para e inicia de novo).
// Se o serviço já estiver parado, apenas inicia (evita erro falso de Stop).
func RestartService(name string) error {
	if err := StopService(name); err != nil && !isStoppedError(err) {
		return fmt.Errorf("stop: %w", err)
	}
	return StartService(name)
}

// isStoppedError detecta quando o serviço já está parado (Stop retorna erro,
// mas o objetivo — serviço parado — já foi atingido). Usado por RestartService
// para não falhar ao reiniciar um serviço que não estava em execução.
func isStoppedError(err error) bool {
	// ERROR_SERVICE_NOT_ACTIVE (1062): o serviço não está em execução.
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == 1062
}
