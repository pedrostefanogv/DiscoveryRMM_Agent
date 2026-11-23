//go:build windows

package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"discovery/app/core/nettable"
	"discovery/app/core/sysctrl"
	"discovery/app/core/windowsupdate"
)

// listServicesNative lista os servicos Windows via sysctrl.
func listServicesNative() (json.RawMessage, error) {
	services, err := sysctrl.ListServices()
	if err != nil {
		return nil, err
	}
	return json.Marshal(services)
}

// getServiceNative localiza um servico por nome ou display name.
func getServiceNative(name string) (json.RawMessage, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("name nao pode ser vazio")
	}
	services, err := sysctrl.ListServices()
	if err != nil {
		return nil, err
	}
	for _, svc := range services {
		if strings.EqualFold(svc.Name, name) || strings.EqualFold(svc.DisplayName, name) {
			return json.Marshal(svc)
		}
	}
	return nil, fmt.Errorf("servico nao encontrado: %s", name)
}

func applyServiceActionNative(name string, action sysctrl.ServiceAction) error {
	res := sysctrl.ApplyServiceAction(name, action, sysctrl.DetectProductType())
	if !res.Decision.Allowed {
		return fmt.Errorf("ação recusada pelo guard de segurança: %s", res.Decision.Reason)
	}
	return res.Err
}

func startServiceNative(name string) error {
	return applyServiceActionNative(name, sysctrl.ActionStart)
}
func stopServiceNative(name string) error { return applyServiceActionNative(name, sysctrl.ActionStop) }
func restartServiceNative(name string) error {
	return applyServiceActionNative(name, sysctrl.ActionRestart)
}

// listProcessesNative lista os processos em execucao via sysctrl.
func listProcessesNative() (json.RawMessage, error) {
	procs, err := sysctrl.ListProcesses()
	if err != nil {
		return nil, err
	}
	return json.Marshal(procs)
}

func killProcessNative(pid uint32) error { return sysctrl.KillProcess(pid) }

// systemInfoNative agrega RAM/CPU do host (sysctrl.GetSystemInfo).
func systemInfoNative() map[string]any {
	si := sysctrl.GetSystemInfo()
	return map[string]any{
		"totalMemoryBytes": si.TotalMemoryBytes,
		"usedMemoryBytes":  si.UsedMemoryBytes,
		"memoryPercent":    si.MemoryPercent,
		"cpuPercent":       si.CpuPercent,
	}
}

func windowsUpdateStatusNative() (any, error) {
	return windowsupdate.ProbeLocalState(), nil
}

func windowsUpdatePendingRebootNative() (any, error) {
	st := windowsupdate.ProbeLocalState()
	return map[string]any{
		"pendingReboot": st.RebootPending,
		"reasons":       st.RebootReasons,
	}, nil
}

// connectionsNative lista conexoes TCP/UDP (IPv4) com PID e estado.
func connectionsNative(limit int) []map[string]any {
	if limit <= 0 {
		limit = 200
	}
	out := make([]map[string]any, 0, limit)
	for _, row := range nettable.TCPRows(nettable.AfInet) {
		if len(out) >= limit {
			return out
		}
		out = append(out, map[string]any{
			"protocol": "TCP",
			"local":    netAddress(row.LocalAddr, row.LocalPort),
			"remote":   netAddress(row.RemoteAddr, row.RemotePort),
			"state":    nettable.TCPStateName(row.State),
			"pid":      row.OwningPid,
		})
	}
	for _, row := range nettable.UDPRows(nettable.AfInet) {
		if len(out) >= limit {
			return out
		}
		out = append(out, map[string]any{
			"protocol": "UDP",
			"local":    netAddress(row.LocalAddr, row.LocalPort),
			"remote":   "",
			"state":    "",
			"pid":      row.OwningPid,
		})
	}
	return out
}

// netAddress monta "ip:porta" a partir de um endereco MIB e da porta bruta.
func netAddress(addr [4]byte, port uint32) string {
	return net.IPv4(addr[0], addr[1], addr[2], addr[3]).String() + ":" + fmt.Sprint(nettable.Port(port))
}
