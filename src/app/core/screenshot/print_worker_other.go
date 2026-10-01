//go:build !windows

package screenshot

import "io"

// RunPrintWorker não tem equivalente fora do Windows.
func RunPrintWorker(args []string) int { return 1 }

// RunPrintServer não tem equivalente fora do Windows.
func RunPrintServer(in io.Reader, out io.Writer) int { return 1 }
