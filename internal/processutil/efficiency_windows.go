//go:build windows

package processutil

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// ProcessPowerThrottling info class for SetProcessInformation (winnt.h).
	processPowerThrottlingClass = 4
	// ThreadPowerThrottling info class for SetThreadInformation (winnt.h).
	threadPowerThrottlingClass = 3

	powerThrottleVersion   = 1
	powerThrottleExecSpeed = 0x1 // PROCESS/THREAD_POWER_THROTTLING_EXECUTION_SPEED
	// PROCESS_POWER_THROTTLING_IGNORE_TIMER_RESOLUTION — process-only, required for green leaf.
	processThrottleIgnoreTimer = 0x4

	// IDLE_PRIORITY_CLASS / NORMAL_PRIORITY_CLASS — what Task Manager checks for the EcoQoS leaf.
	idlePriorityClass   = 0x00000040
	normalPriorityClass = 0x00000020
)

type processPowerThrottlingState struct {
	Version     uint32
	ControlMask uint32
	StateMask   uint32
}

var (
	modKernel32               = windows.NewLazySystemDLL("kernel32.dll")
	modPsapi                  = windows.NewLazySystemDLL("psapi.dll")
	procSetProcessInformation = modKernel32.NewProc("SetProcessInformation")
	procSetThreadInformation  = modKernel32.NewProc("SetThreadInformation")
	procSetPriorityClass      = modKernel32.NewProc("SetPriorityClass")
	procEmptyWorkingSet       = modPsapi.NewProc("EmptyWorkingSet")
)

// SetEfficiencyMode enables or disables Windows EcoQoS (Efficiency Mode).
// When enabled, Task Manager shows the green leaf icon next to the process.
// Requires Windows 11 / Server 2022+ for full effect; gracefully degrades on older versions.
// Returns whether any efficiency API was available on this system.
func SetEfficiencyMode(enable bool) (bool, error) {
	procHandle := windows.CurrentProcess()
	threadHandle := windows.CurrentThread()

	var supported bool
	var errs []string

	// Step 1 — Process-level EcoQoS throttling.
	// EXECUTION_SPEED + IGNORE_TIMER_RESOLUTION is the combination that produces the green leaf.
	if procSetProcessInformation.Find() == nil {
		supported = true
		state := processPowerThrottlingState{
			Version:     powerThrottleVersion,
			ControlMask: powerThrottleExecSpeed | processThrottleIgnoreTimer,
		}
		if enable {
			state.StateMask = powerThrottleExecSpeed | processThrottleIgnoreTimer
		}
		r1, _, callErr := procSetProcessInformation.Call(
			uintptr(procHandle),
			uintptr(processPowerThrottlingClass),
			uintptr(unsafe.Pointer(&state)),
			unsafe.Sizeof(state),
		)
		if r1 == 0 {
			errs = append(errs, fmt.Sprintf("SetProcessInformation: %v", callErr))
		}
	}

	// Step 2 — Thread-level throttling (EXECUTION_SPEED only; IGNORE_TIMER is process-only).
	if procSetThreadInformation.Find() == nil {
		supported = true
		state := processPowerThrottlingState{
			Version:     powerThrottleVersion,
			ControlMask: powerThrottleExecSpeed,
		}
		if enable {
			state.StateMask = powerThrottleExecSpeed
		}
		r1, _, callErr := procSetThreadInformation.Call(
			uintptr(threadHandle),
			uintptr(threadPowerThrottlingClass),
			uintptr(unsafe.Pointer(&state)),
			unsafe.Sizeof(state),
		)
		if r1 == 0 {
			errs = append(errs, fmt.Sprintf("SetThreadInformation: %v", callErr))
		}
	}

	// Step 3 — Priority class: IDLE_PRIORITY_CLASS (0x40) is what Task Manager checks for the
	// EcoQoS green leaf. PROCESS_MODE_BACKGROUND_BEGIN is a different, older mechanism and is
	// NOT recognised by Task Manager as "Efficiency Mode".
	if procSetPriorityClass.Find() == nil {
		supported = true
		priority := uintptr(normalPriorityClass)
		if enable {
			priority = uintptr(idlePriorityClass)
		}
		r1, _, callErr := procSetPriorityClass.Call(uintptr(procHandle), priority)
		if r1 == 0 {
			errs = append(errs, fmt.Sprintf("SetPriorityClass: %v", callErr))
		}
	}

	if len(errs) > 0 {
		return supported, fmt.Errorf("%s", joinErrs(errs))
	}
	return supported, nil
}

func joinErrs(errs []string) string {
	out := errs[0]
	for _, e := range errs[1:] {
		out += "; " + e
	}
	return out
}

// TrimCurrentProcessWorkingSet asks Windows to reclaim as much resident memory as possible.
func TrimCurrentProcessWorkingSet() error {
	handle := windows.CurrentProcess()
	if procEmptyWorkingSet.Find() != nil {
		return fmt.Errorf("EmptyWorkingSet indisponivel")
	}
	r1, _, callErr := procEmptyWorkingSet.Call(uintptr(handle))
	if r1 == 0 {
		return fmt.Errorf("EmptyWorkingSet: %w", callErr)
	}
	return nil
}
