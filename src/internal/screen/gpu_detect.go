//go:build windows

package screen

// GPUCapability indica capacidades da GPU detectada.
type GPUCapability struct {
	DXGIAvailable bool
	MemoryMB      int64
}

// DetectGPU detecta se DXGI esta disponivel.
func DetectGPU() GPUCapability {
	// Tenta criar um capturador DXGI para validar disponibilidade
	_, err := NewDXGICapturer(0)
	dxgiAvailable := err == nil

	// GPU memory seria detectada via IDXGIAdapter::QueryVideoMemoryInfo
	// Placeholder: assumir 256MB para fallback seguro
	memoryMB := int64(256)

	return GPUCapability{
		DXGIAvailable: dxgiAvailable,
		MemoryMB:      memoryMB,
	}
}
