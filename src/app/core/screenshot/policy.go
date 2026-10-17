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
	// AllowAiCapture liga/desliga os PEDIDOS de captura feitos pela IA.
	// NÃO é autorização: com true (padrão) a IA pergunta a CADA captura; com
	// false a tool é recusada sem abrir o diálogo.
	AllowAiCapture *bool `json:"allowAiCapture,omitempty"`
	// AllowSessionCapture é a permissão "permitir sempre": com true a IA deixa
	// de pedir autorização a cada captura até o usuário desativar (ou voltar ao
	// padrão). É PERSISTIDA no arquivo da política — sobrevive a reinício e
	// atualização do agente — e nasce DESLIGADA (nil/false = pergunta sempre).
	AllowSessionCapture *bool `json:"allowSessionCapture,omitempty"`
	// CopyOnConfirm copia a captura para a área de transferência ao confirmar a
	// seleção no overlay. Opt-in: o padrão é não mexer no clipboard do usuário.
	CopyOnConfirm *bool `json:"copyOnConfirm,omitempty"`
	// GalleryEnabled liga a galeria local ("Discovery Capturas"). Default true.
	GalleryEnabled *bool `json:"galleryEnabled,omitempty"`
	// GalleryMaxItems limita quantos itens a galeria mantém. Default 50,
	// clampado em 1..500.
	GalleryMaxItems int `json:"galleryMaxItems,omitempty"`
}

const (
	// DefaultGalleryMaxItems é o teto padrão de itens da galeria.
	DefaultGalleryMaxItems = 50
	// MinGalleryMaxItems/MaxGalleryMaxItems delimitam o ajuste permitido na UI.
	MinGalleryMaxItems = 1
	MaxGalleryMaxItems = 500
)

// DefaultPolicy devolve a política padrão do produto: sem bloqueios, tela
// inteira permitida, janela do agente oculta durante a captura, formato
// automático e limite de 10 capturas a cada 1 minuto. É o estado de uma
// instalação nova e o alvo do botão "Voltar ao padrão" da UI de privacidade.
func DefaultPolicy() Policy {
	full := true
	requireWindow := false
	hideWindow := true
	allowAi := true
	allowAlways := false
	copyOnConfirm := false
	galleryEnabled := true
	return Policy{
		BlockedProcesses:     []string{},
		AllowFullScreen:      &full,
		RequireWindow:        &requireWindow,
		MaxCapturesPerWindow: DefaultMaxCapturesPerWindow,
		WindowMinutes:        DefaultCaptureWindowMinutes,
		HideAgentWindow:      &hideWindow,
		ImageFormat:          FormatAuto,
		AllowAiCapture:       &allowAi,
		AllowSessionCapture:  &allowAlways,
		CopyOnConfirm:        &copyOnConfirm,
		GalleryEnabled:       &galleryEnabled,
		GalleryMaxItems:      DefaultGalleryMaxItems,
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

// AiCaptureAllowed indica se a IA pode PEDIR capturas. Default true — e mesmo
// habilitado, cada pedido exige autorização individual do usuário.
func (p Policy) AiCaptureAllowed() bool {
	if p.AllowAiCapture == nil {
		return true
	}
	return *p.AllowAiCapture
}

// SessionCaptureAllowed indica se a IA está dispensada de pedir autorização a
// cada captura ("permitir sempre"). Default false: sem escolha explícita do
// usuário (nil), a IA continua perguntando em cada pedido.
func (p Policy) SessionCaptureAllowed() bool {
	if p.AllowSessionCapture == nil {
		return false
	}
	return *p.AllowSessionCapture
}

// HideWindowOnCapture indica se a janela do agente deve ser ocultada durante a
// captura. Default true quando o campo não está definido.
func (p Policy) HideWindowOnCapture() bool {
	if p.HideAgentWindow == nil {
		return true
	}
	return *p.HideAgentWindow
}

// CopyOnConfirmEnabled indica se a captura deve ir para a área de
// transferência ao confirmar. Default false (opt-in).
func (p Policy) CopyOnConfirmEnabled() bool {
	if p.CopyOnConfirm == nil {
		return false
	}
	return *p.CopyOnConfirm
}

// GalleryAllowed indica se a galeria local está habilitada. Default true.
func (p Policy) GalleryAllowed() bool {
	if p.GalleryEnabled == nil {
		return true
	}
	return *p.GalleryEnabled
}

// GalleryItemLimit devolve o limite de itens da galeria normalizado (1..500).
func (p Policy) GalleryItemLimit() int {
	limit := p.GalleryMaxItems
	if limit <= 0 {
		return DefaultGalleryMaxItems
	}
	if limit < MinGalleryMaxItems {
		return MinGalleryMaxItems
	}
	if limit > MaxGalleryMaxItems {
		return MaxGalleryMaxItems
	}
	return limit
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
	allowAi := out.AiCaptureAllowed()
	copyOnConfirm := out.CopyOnConfirmEnabled()
	galleryEnabled := out.GalleryAllowed()
	out.AllowFullScreen = &full
	out.RequireWindow = &requireWindow
	out.HideAgentWindow = &hideWindow
	out.AllowAiCapture = &allowAi
	out.CopyOnConfirm = &copyOnConfirm
	out.GalleryEnabled = &galleryEnabled
	out.GalleryMaxItems = out.GalleryItemLimit()
	out.MaxCapturesPerWindow = out.LimitMax()
	out.WindowMinutes = int(out.LimitWindow().Minutes())
	out.ImageFormat = NormalizeFormat(out.ImageFormat)
	return out
}
