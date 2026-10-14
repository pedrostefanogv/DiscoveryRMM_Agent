//go:build windows

package screen

// GPUCapability indica capacidades da GPU detectada.
type GPUCapability struct {
	DXGIAvailable bool
	MemoryMB      int64
}

// DetectGPU detecta se DXGI esta disponivel.
func DetectGPU() GPUCapability {
	// Tenta criar um capturador DXGI para validar disponibilidade.
	//
	// O capturador de teste PRECISA ser fechado: cada NewDXGICapturer cria um
	// device D3D11 e uma duplicação de output; sem Close o device (e suas
	// superfícies, dezenas de MB em 4K) ficava retido até o processo morrer — e
	// esta função roda a CADA sessão remota (session_screen.go).
	c, err := NewDXGICapturer(0)
	dxgiAvailable := err == nil
	if c != nil {
		_ = c.Close()
	}

	// GPU memory seria detectada via IDXGIAdapter::QueryVideoMemoryInfo
	// Placeholder: assumir 256MB para fallback seguro
	memoryMB := int64(256)

	return GPUCapability{
		DXGIAvailable: dxgiAvailable,
		MemoryMB:      memoryMB,
	}
}
