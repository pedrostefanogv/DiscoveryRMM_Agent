//go:build windows

package sysctrl

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
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
	// Métricas de consumo do processo do serviço (quando PID > 0).
	// Coletadas em process_metrics_windows.go; vazias para serviços parados.
	CpuPercent  float64 `json:"cpuPercent"`
	MemoryBytes uint64  `json:"memoryBytes"`
	IoReadBps   float64 `json:"ioReadBps"` // taxa de leitura (bytes/s)
	IoWriteBps  float64 `json:"ioWriteBps"`
	Connections uint32  `json:"connections"`
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
//
// Usa direitos MÍNIMOS (SC_MANAGER_ENUMERATE_SERVICE + SERVICE_QUERY_CONFIG) em
// vez de SC_MANAGER_ALL_ACCESS. Isso permite listar serviços mesmo quando o
// agente NÃO está elevado (processo "asInvoker" de usuário padrão) — o
// mgr.Connect() usava SC_MANAGER_ALL_ACCESS e falhava com "Acesso negado".
func ListServices() ([]ServiceInfo, error) {
	h, err := windows.OpenSCManager(nil, nil,
		windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return nil, fmt.Errorf("OpenSCManager: %w", err)
	}
	defer windows.CloseServiceHandle(h)

	// Enumera com estado/processo em uma única chamada (nome, display, estado, PID).
	entries, err := enumServicesStatus(h)
	if err != nil {
		return nil, fmt.Errorf("EnumServicesStatusEx: %w", err)
	}

	services := make([]ServiceInfo, 0, len(entries))
	for _, e := range entries {
		name := windows.UTF16PtrToString(e.ServiceName)
		si := ServiceInfo{
			Name:        name,
			DisplayName: windows.UTF16PtrToString(e.DisplayName),
			State:       stateName(svc.State(e.ServiceStatusProcess.CurrentState)),
			PID:         e.ServiceStatusProcess.ProcessId,
		}
		// StartType/BinaryPath exigem SERVICE_QUERY_CONFIG; alguns serviços
		// protegidos negam — nesses casos os campos ficam vazios.
		if startType, binaryPath, cfgErr := queryServiceConfig(h, name); cfgErr == nil {
			si.StartType = startTypeName(startType)
			si.BinaryPath = binaryPath
		}
		services = append(services, si)
	}
	// Enriquecimento com métricas do processo do serviço (CPU/RAM/disco/
	// rede) quando o serviço está em execução (PID > 0).
	return enrichServiceMetrics(services), nil
}

// enumServicesStatus enumera serviços via EnumServicesStatusEx (SC_ENUM_PROCESS_INFO),
// retornando nome, display name, estado e PID em uma única chamada.
func enumServicesStatus(scm windows.Handle) ([]windows.ENUM_SERVICE_STATUS_PROCESS, error) {
	var bytesNeeded, servicesReturned uint32
	var buf []byte
	for {
		var p *byte
		if len(buf) > 0 {
			p = &buf[0]
		}
		err := windows.EnumServicesStatusEx(scm, windows.SC_ENUM_PROCESS_INFO,
			windows.SERVICE_WIN32, windows.SERVICE_STATE_ALL,
			p, uint32(len(buf)), &bytesNeeded, &servicesReturned, nil, nil)
		if err == nil {
			break
		}
		if err != syscall.ERROR_MORE_DATA {
			return nil, err
		}
		if bytesNeeded <= uint32(len(buf)) {
			return nil, err
		}
		buf = make([]byte, bytesNeeded)
	}
	if servicesReturned == 0 {
		return nil, nil
	}
	return unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0])), int(servicesReturned)), nil
}

// queryServiceConfig lê o start type e o caminho do binário de um serviço
// usando apenas SERVICE_QUERY_CONFIG (não exige privilégios de administrador).
func queryServiceConfig(scm windows.Handle, name string) (uint32, string, error) {
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, "", err
	}
	h, err := windows.OpenService(scm, namePtr, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseServiceHandle(h)

	var p *windows.QUERY_SERVICE_CONFIG
	n := uint32(1024)
	for {
		b := make([]byte, n)
		p = (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&b[0]))
		err = windows.QueryServiceConfig(h, p, n, &n)
		if err == nil {
			break
		}
		if err != syscall.ERROR_INSUFFICIENT_BUFFER {
			return 0, "", err
		}
		if n <= uint32(len(b)) {
			return 0, "", err
		}
	}
	return p.StartType, windows.UTF16PtrToString(p.BinaryPathName), nil
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
