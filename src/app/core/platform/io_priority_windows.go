//go:build windows

package platform

import (
	"os"
	"syscall"
)

const (
	// FILE_FLAG_SEQUENTIAL_SCAN = 0x08000000 informa ao Windows cache manager
	// que o padrão de acesso é sequencial, reduzindo pressão no cache de disco.
	fileFlagSequentialScan = 0x08000000
)

// OpenFileSequential abre um arquivo para leitura com FILE_FLAG_SEQUENTIAL_SCAN.
// Esta flag informa ao Windows cache manager que o padrão de acesso é sequencial,
// reduzindo a pressão no cache de disco e prioridade de I/O.
func OpenFileSequential(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|fileFlagSequentialScan,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
