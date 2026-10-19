//go:build windows

package winui

import "testing"

// O prompt de consentimento remoto nasce no canto inferior direito da área útil
// (como um toast), recuado da barra de tarefas pela margem configurada.
func TestBottomRightInArea(t *testing.T) {
	cases := []struct {
		name   string
		area   Rect
		width  int
		height int
		margin int
		wantX  int
		wantY  int
	}{
		{
			name:   "monitor primario 1920x1080 com taskbar",
			area:   Rect{X: 0, Y: 0, W: 1920, H: 1040},
			width:  640,
			height: 400,
			margin: 24,
			wantX:  1256,
			wantY:  616,
		},
		{
			name:   "monitor secundario com origem nao zero",
			area:   Rect{X: 1280, Y: 0, W: 1920, H: 1040},
			width:  640,
			height: 400,
			margin: 24,
			wantX:  2536,
			wantY:  616,
		},
		{
			name:   "margem negativa e tratada como zero",
			area:   Rect{X: 0, Y: 0, W: 1920, H: 1040},
			width:  640,
			height: 400,
			margin: -10,
			wantX:  1280,
			wantY:  640,
		},
		{
			name:   "janela maior que a area nao sai pela esquerda/topo",
			area:   Rect{X: 100, Y: 50, W: 300, H: 200},
			width:  640,
			height: 400,
			margin: 24,
			wantX:  100,
			wantY:  50,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, y := bottomRightInArea(tc.area, tc.width, tc.height, tc.margin)
			if x != tc.wantX || y != tc.wantY {
				t.Fatalf("bottomRightInArea(%+v, %d, %d, %d) = (%d,%d), want (%d,%d)",
					tc.area, tc.width, tc.height, tc.margin, x, y, tc.wantX, tc.wantY)
			}
		})
	}
}
