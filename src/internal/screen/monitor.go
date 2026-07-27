package screen

import "fmt"

// Monitor representa um monitor conectado.
type Monitor struct {
	Index    int
	Name     string
	Width    int
	Height   int
	IsPrimary bool
}

// GetMonitors retorna a lista de monitores conectados.
func GetMonitors() ([]Monitor, error) {
	width, _, _ := procGetSystemMetrics.Call(SM_CXSCREEN)
	height, _, _ := procGetSystemMetrics.Call(SM_CYSCREEN)

	if width == 0 || height == 0 {
		return nil, fmt.Errorf("nao foi possivel detectar monitores")
	}

	return []Monitor{
		{
			Index:     0,
			Name:      fmt.Sprintf("Monitor %dx%d", width, height),
			Width:     int(width),
			Height:    int(height),
			IsPrimary: true,
		},
	}, nil
}
