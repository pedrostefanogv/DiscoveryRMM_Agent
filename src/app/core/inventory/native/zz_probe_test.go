//go:build windows

package native

import (
	"context"
	"fmt"
	"testing"

	"discovery/app/core/power"
)

// derefBool/derefInt existem só para impressão legível no probe.
func derefBool(v *bool) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprintf("%v", *v)
}

func derefInt(v *int) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprintf("%d", *v)
}

// TestZZProbeCollectorsManual é um probe manual (go test -run ZZProbeCollectors -v)
// para validar no runtime real desta máquina os coletores corrigidos.
func TestZZProbeCollectorsManual(t *testing.T) {
	ctx := context.Background()

	printers, perr := collectPrintersNative(ctx)
	fmt.Printf("printers=%d err=%v\n", len(printers), perr)
	for _, p := range printers {
		fmt.Printf("  printer: %q driver=%q port=%q status=%q default=%v net=%v\n",
			p.Name, p.DriverName, p.PortName, p.PrinterStatus, p.IsDefault, p.IsNetworkPrinter)
	}

	monitors, merr := collectMonitorsNative(ctx)
	fmt.Printf("monitors=%d err=%v\n", len(monitors), merr)
	for _, m := range monitors {
		fmt.Printf("  monitor: %q mfr=%q serial=%q status=%q\n", m.Name, m.Manufacturer, m.Serial, m.Status)
	}

	_, mem, gpus, cpus, _, herr := collectHardwareNative(ctx)
	fmt.Printf("hardware err=%v\n", herr)
	fmt.Printf("memoryModules=%d gpus=%d cpuInfo=%d\n", len(mem), len(gpus), len(cpus))
	for _, g := range gpus {
		fmt.Printf("  gpu: %q status=%q\n", g.Name, g.Status)
	}
	for _, m := range mem {
		fmt.Printf("  mem: slot=%q part=%q speed=%d\n", m.Slot, m.PartNumber, m.SpeedMHz)
	}
	for _, c := range cpus {
		fmt.Printf("  cpu: %q cores=%d threads=%d\n", c.Model, c.NumberOfCores, c.LogicalProcessors)
	}

	battery, berr := collectBatteryNative(ctx)
	fmt.Printf("battery=%d err=%v\n", len(battery), berr)

	bitlocker, blerr := collectBitLockerNative(ctx)
	fmt.Printf("bitLocker=%d err=%v\n", len(bitlocker), blerr)

	profile := collectMachineProfileNative(ctx, false)
	fmt.Printf("machineProfile=%+v\n", profile)
	if profile.IsLaptop != nil {
		fmt.Printf("  isLaptop=%v\n", *profile.IsLaptop)
	}
	if profile.PCSystemType != nil {
		fmt.Printf("  pcSystemType=%v\n", *profile.PCSystemType)
	}
	if st, ok := power.Read(); ok {
		fmt.Printf("  power: batteryKnown=%v hasBattery=%v onAC=%v charging=%v percent=%v\n",
			st.BatteryKnown, st.HasBattery, derefBool(st.OnAC), derefBool(st.Charging), derefInt(st.Percent))
	}

	rawCPU, cerr := wmiQuery("root\\cimv2", "SELECT Name, NumberOfCores, NumberOfLogicalProcessors FROM Win32_Processor")
	fmt.Printf("rawCPU rows=%d err=%v\n", len(rawCPU), cerr)
	for i, r := range rawCPU {
		fmt.Printf("  rawCPU[%d] keys=%v name=%q cores=%v threads=%v\n", i, len(r), r["Name"], r["NumberOfCores"], r["NumberOfLogicalProcessors"])
	}

	rawMem, merr := wmiQuery("root\\cimv2", "SELECT BankLabel, Capacity, Speed, DeviceLocator FROM Win32_PhysicalMemory")
	fmt.Printf("rawMem rows=%d err=%v\n", len(rawMem), merr)
	for i, r := range rawMem {
		fmt.Printf("  rawMem[%d] loc=%v cap=%v speed=%v\n", i, r["DeviceLocator"], r["Capacity"], r["Speed"])
	}
}
