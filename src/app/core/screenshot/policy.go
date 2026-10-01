package screenshot

import (
	"path/filepath"
	"strings"
	"time"
)

// Policy é a política local de captura de tela (persistida no agent e
// editável na UI de privacidade). O servidor poderá empurrar esses valores no
// futuro; por ora a fonte é local.
type Policy struct {
	// BlockedProcesses bloqueia captura direcionada a um processo. A comparação
	// ignora caixa e sufixo .exe e aceita trecho do nome (ex.: "keepass" bloqueia
	// keepassxc.exe).
	BlockedProcesses []string `json:"blockedProcesses"`
	// AllowFullScreen: false impede capturas de tela inteira/monitor/região
	// pedidas pela IA (o usuário ainda pode capturar manualmente pelo overlay).
	AllowFullScreen *bool `json:"allowFullScreen,omitempty"`
	// RequireWindow: true exige que a IA capture uma janela específica
	// (nenhuma captura de tela inteira).
	RequireWindow *bool `json:"requireWindow,omitempty"`
	// MaxCapturesPerWindow limita capturas da IA por janela de tempo.
	MaxCapturesPerWindow int `json:"maxCapturesPerWindow,omitempty"`
	// WindowMinutes é o tamanho da janela de tempo do limite.
	WindowMinutes int `json:"windowMinutes,omitempty"`
	// HideAgentWindow oculta a janela do agente durante a captura para que a UI
	// do próprio agente não apareça no print. Default true.
	HideAgentWindow *bool `json:"hideAgentWindow,omitempty"`
	// ImageFormat é a preferência de formato da imagem final: "auto" (WebP
	// lossless quando menor que PNG) ou "png" (portabilidade máxima — use se
	// algum provedor de visão recusar WebP). Vazio = auto.
	ImageFormat string `json:"imageFormat,omitempty"`
}

// DefaultPolicy devolve a política conservadora padrão: sem bloqueios, tela
// inteira permitida e limite de 10 capturas a cada 5 minutos.
func DefaultPolicy() Policy {
	full := true
	requireWindow := false
	hideWindow := true
	return Policy{
		BlockedProcesses:     []string{},
		AllowFullScreen:      &full,
		RequireWindow:        &requireWindow,
		MaxCapturesPerWindow: DefaultMaxCapturesPerWindow,
		WindowMinutes:        DefaultCaptureWindowMinutes,
		HideAgentWindow:      &hideWindow,
	}
}

// HideWindowOnCapture indica se a janela do agente deve ser ocultada durante a
// captura (para não sair no print). Default true.

// FullScreenAllowed indica se a IA pode capturar tela inteira/monitor/região.
func (p Policy) FullScreenAllowed() bool {
	if p.AllowFullScreen == nil {
		return true
	}
	return *p.AllowFullScreen
}

// WindowRequired indica se a IA deve sempre apontar uma janela específica.
func (p Policy) WindowRequired() bool {
	if p.RequireWindow == nil {
		return false
	}
	return *p.RequireWindow
}

// HideWindowOnCapture indica se a janela do agente deve ser ocultada durante a
// captura. Default true quando o campo não está definido.
func (p Policy) HideWindowOnCapture() bool {
	if p.HideAgentWindow == nil {
		return true
	}
	return *p.HideAgentWindow
}

// LimitMax devolve o teto de capturas normalizado.
func (p Policy) LimitMax() int {
	if p.MaxCapturesPerWindow <= 0 {
		return DefaultMaxCapturesPerWindow
	}
	if p.MaxCapturesPerWindow > 120 {
		return 120
	}
	return p.MaxCapturesPerWindow
}

// LimitWindow devolve a janela de tempo normalizada.
func (p Policy) LimitWindow() time.Duration {
	minutes := p.WindowMinutes
	if minutes <= 0 {
		minutes = DefaultCaptureWindowMinutes
	}
	if minutes > 1440 {
		minutes = 1440
	}
	return time.Duration(minutes) * time.Minute
}

// IsProcessBlocked informa se o processo está na blocklist.
func (p Policy) IsProcessBlocked(processName string) bool {
	name := NormalizeProcessName(processName)
	if name == "" {
		return false
	}
	for _, blocked := range p.BlockedProcesses {
		entry := NormalizeProcessName(blocked)
		if entry == "" {
			continue
		}
		if name == entry || strings.Contains(name, entry) {
			return true
		}
	}
	return false
}

// NormalizeProcessName deixa o nome comparável: minúsculo, sem diretório e sem
// extensão .exe.
func NormalizeProcessName(name string) string {
	v := strings.ToLower(strings.TrimSpace(name))
	if v == "" {
		return ""
	}
	if base := filepath.Base(v); base != "." && base != string(filepath.Separator) {
		v = base
	}
	v = strings.TrimSuffix(v, ".exe")
	return strings.TrimSpace(v)
}

// Normalize limpa/limita a política recebida da UI (entradas vazias,
// duplicatas e valores fora de faixa).
func (p Policy) Normalize() Policy {
	out := p
	seen := make(map[string]bool, len(p.BlockedProcesses))
	blocked := make([]string, 0, len(p.BlockedProcesses))
	for _, item := range p.BlockedProcesses {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		key := NormalizeProcessName(name)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		blocked = append(blocked, name)
		if len(blocked) >= 100 {
			break
		}
	}
	out.BlockedProcesses = blocked
	full := out.FullScreenAllowed()
	requireWindow := out.WindowRequired()
	hideWindow := out.HideWindowOnCapture()
	out.AllowFullScreen = &full
	out.RequireWindow = &requireWindow
	out.HideAgentWindow = &hideWindow
	out.MaxCapturesPerWindow = out.LimitMax()
	out.WindowMinutes = int(out.LimitWindow().Minutes())
	out.ImageFormat = NormalizeFormat(out.ImageFormat)
	return out
}
