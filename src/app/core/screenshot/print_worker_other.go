//go:build !windows

package screenshot

// RunPrintWorker não tem equivalente fora do Windows.
func RunPrintWorker(args []string) int { return 1 }
