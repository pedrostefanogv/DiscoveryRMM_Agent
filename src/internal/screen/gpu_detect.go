package screen

// GPUCapability indica capacidades da GPU detectada.
type GPUCapability struct {
	DXGIAvailable        bool
	MediaFoundationH264  bool
	MemoryMB             int64
}

// DetectGPU detecta se DXGI e Media Foundation estao disponiveis.
func DetectGPU() GPUCapability {
	// Tenta criar um capturador DXGI para validar disponibilidade
	_, err := NewDXGICapturer(0)
	dxgiAvailable := err == nil

	// Media Foundation H.264 disponibilidade:
	// Verifica via syscall se o encoder MF H.264 existe
	mfH264Available := isMediaFoundationH264Available()

	// GPU memory seria detectada via IDXGIAdapter::QueryVideoMemoryInfo
	// Placeholder: assumir 256MB para fallback seguro
	memoryMB := int64(256)

	return GPUCapability{
		DXGIAvailable:       dxgiAvailable,
		MediaFoundationH264:  mfH264Available,
		MemoryMB:             memoryMB,
	}
}

// isMediaFoundationH264Available verifica se o encoder H.264 via MF esta disponivel.
func isMediaFoundationH264Available() bool {
	// Implementacao real: MFStartup + MFTEnum com MFT_CATEGORY_VIDEO_ENCODER e MFVideoFormat_H264.
	// Por enquanto, retorna false para usar fallback JPEG/WebP.
	return false
}
