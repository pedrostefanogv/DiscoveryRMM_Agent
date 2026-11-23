//go:build windows

package native

import (
	"context"
	"fmt"

	"discovery/app/core/models"
	"discovery/app/core/power"
)

// NOTA sobre Win32_SystemEnclosure.ChassisTypes (SMBIOS chassis type): NÃO é
// usado. O helper COM atual (normalizeWMIValue) não sabe ler SAFEARRAY e o
// campo chega como string VAZIA — verificado no probe ZZProbeMachineType nesta
// base. A classificação usa PCSystemType/PCSystemTypeEx, que são escalares
// confiáveis, e a presença FÍSICA de bateria via GetSystemPowerStatus.

// Classes de PCSystemType (Win32_ComputerSystem). Nomes do PCSystemTypeEx.
const (
	pcTypeDesktop     = 1
	pcTypeMobile      = 2
	pcTypeWorkstation = 3
	pcTypeEntServer   = 4
	pcTypeSohoServer  = 5
	pcTypeAppliance   = 6
	pcTypePerfServer  = 7
	pcTypeSlate       = 8
	pcTypeConvertible = 9
	pcTypeDetachable  = 10
)

func boolPtrValue(v bool) *bool { return &v }

// collectMachineProfileNative classifica o formato físico da máquina.
//
// isServer vem do inventário de SO (ProductType) e tem precedência: servidor
// nunca é notebook, nem quando uma UPS/gerenciador de energia aparece como
// Win32_Battery.
func collectMachineProfileNative(ctx context.Context, isServer bool) models.MachineProfile {
	_ = ctx

	pcSystemType, hasPCSystemType := readPCSystemType()

	var hasBattery *bool
	if status, ok := power.Read(); ok && status.BatteryKnown {
		v := status.HasBattery
		hasBattery = &v
	}

	return classifyMachineProfile(pcSystemType, hasPCSystemType, hasBattery, isServer)
}

// readPCSystemType lê PCSystemTypeEx (preferido, mais específico) e cai para
// PCSystemType. ok=false quando nenhum dos dois trouxe valor útil — nesse caso
// a classificação NÃO pode afirmar desktop.
func readPCSystemType() (int, bool) {
	rows, err := wmiQuery(wmiNamespace, "SELECT PCSystemType, PCSystemTypeEx FROM Win32_ComputerSystem")
	if err != nil || len(rows) == 0 {
		return 0, false
	}
	row := rows[0]
	if n := wmiValueInt(row, "PCSystemTypeEx"); n > 0 {
		return n, true
	}
	if n := wmiValueInt(row, "PCSystemType"); n > 0 {
		return n, true
	}
	return 0, false
}

// wmiValueInt devolve o inteiro do campo distinguindo AUSENTE de zero: o
// helper wmiInt devolve 0 nos dois casos, e aqui 0 significa "não reportado",
// não "desktop".
func wmiValueInt(row map[string]any, key string) int {
	v, ok := row[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case int64:
		return int(t)
	case int:
		return t
	case float64:
		return int(t)
	case string:
		n := 0
		if _, err := fmt.Sscanf(t, "%d", &n); err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// classifyMachineProfile é a REGRA PURA de classificação (sem WMI), para poder
// ser testada em qualquer máquina:
//
//  1. Servidor (OS.IsServer) ou classe de servidor no PCSystemType → servidor.
//  2. Slate/Convertible/Detachable → tablet (é móvel, mas não é notebook).
//  3. Bateria presente → notebook. Cobre OEMs que reportam PCSystemType 1/3 em
//     notebook e máquinas antigas sem PCSystemType. Trade-off conhecido: um
//     desktop com UPS exposta como Win32_Battery cai aqui — aceitável, porque
//     o sinal de presença física de bateria é o mais forte disponível via API
//     do SO e o caso é raro.
//  4. PCSystemType Mobile sem bateria legível → notebook.
//  5. Desktop SÓ com sinal positivo: classe de desktop/estação E ausência de
//     bateria CONFIRMADA. Sem isso fica desconhecido — VM e agente antigo não
//     podem virar "desktop" por omissão.
func classifyMachineProfile(pcSystemType int, hasPCSystemType bool, hasBattery *bool, isServer bool) models.MachineProfile {
	profile := models.MachineProfile{}
	if hasPCSystemType {
		v := pcSystemType
		profile.PCSystemType = &v
	}
	profile.HasBattery = hasBattery

	if isServer || (hasPCSystemType && isServerPCSystemType(pcSystemType)) {
		profile.IsLaptop = boolPtrValue(false)
		profile.MachineType = models.MachineTypeServer
		return profile
	}

	if hasPCSystemType && isTabletPCSystemType(pcSystemType) {
		profile.IsLaptop = boolPtrValue(true)
		profile.MachineType = models.MachineTypeTablet
		return profile
	}

	if hasBattery != nil && *hasBattery {
		profile.IsLaptop = boolPtrValue(true)
		profile.MachineType = models.MachineTypeNotebook
		return profile
	}

	if hasPCSystemType && pcSystemType == pcTypeMobile {
		profile.IsLaptop = boolPtrValue(true)
		profile.MachineType = models.MachineTypeNotebook
		return profile
	}

	if hasPCSystemType && isDesktopPCSystemType(pcSystemType) && hasBattery != nil && !*hasBattery {
		profile.IsLaptop = boolPtrValue(false)
		profile.MachineType = models.MachineTypeDesktop
		return profile
	}

	profile.MachineType = models.MachineTypeUnknown
	return profile
}

func isServerPCSystemType(t int) bool {
	switch t {
	case pcTypeEntServer, pcTypeSohoServer, pcTypePerfServer:
		return true
	default:
		return false
	}
}

func isTabletPCSystemType(t int) bool {
	switch t {
	case pcTypeSlate, pcTypeConvertible, pcTypeDetachable:
		return true
	default:
		return false
	}
}

func isDesktopPCSystemType(t int) bool {
	switch t {
	case pcTypeDesktop, pcTypeWorkstation, pcTypeAppliance:
		return true
	default:
		return false
	}
}
