//go:build windows

package inventory

import "golang.org/x/sys/windows"

// freeDiskBytes devolve o espaço livre (em bytes) do volume que contém dir.
// Usado antes de baixar instaladores grandes para evitar falha no meio do
// download por falta de espaço.
func freeDiskBytes(dir string) (uint64, error) {
	path, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}

	var freeAvailableToCaller, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	if err := windows.GetDiskFreeSpaceEx(path, &freeAvailableToCaller, &totalNumberOfBytes, &totalNumberOfFreeBytes); err != nil {
		return 0, err
	}

	return freeAvailableToCaller, nil
}
