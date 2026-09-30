// Package screenshot implementa a captura de tela assistida do DiscoveryRMM:
// enumeração de janelas abertas, captura de tela cheia/região/janela e o
// mecanismo de consentimento exigido antes de a IA capturar a tela do usuário.
//
// A captura reutiliza o pipeline já existente em app/core/screen (DXGI/GDI,
// tone mapping HDR e encoders) para não duplicar código de baixo nível.
package screenshot

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Modos de captura aceitos pelos bindings e pela tool MCP capture_screenshot.
const (
	ModeFull        = "full"        // desktop virtual inteiro (todos os monitores)
	ModeMonitor     = "monitor"     // um monitor específico
	ModeRegion      = "region"      // região retangular informada
	ModeWindow      = "window"      // janela específica (PrintWindow)
	ModeFocused     = "focused"     // janela em foco (o app resolve para window)
	ModeInteractive = "interactive" // usuário seleciona área/janela no overlay
)

// WindowInfo descreve uma janela de nível superior visível no desktop atual.
type WindowInfo struct {
	Handle      uint64 `json:"handle"`
	Title       string `json:"title"`
	ProcessName string `json:"processName"`
	PID         uint32 `json:"pid"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Minimized   bool   `json:"minimized"`
	Foreground  bool   `json:"foreground"`
	IsSelf      bool   `json:"isSelf"`
	// Blocked indica que o processo está na blocklist da política local de
	// privacidade (a IA não deve capturar esta janela).
	Blocked bool `json:"blocked,omitempty"`
	ZOrder  int  `json:"zOrder"`
}

// MonitorInfo descreve um monitor no desktop virtual (coordenadas físicas).
type MonitorInfo struct {
	Index   int    `json:"index"`
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Name    string `json:"name,omitempty"`
	Primary bool   `json:"primary"`
}

// Request descreve uma solicitação de captura.
type Request struct {
	Mode         string
	MonitorIndex int
	X            int
	Y            int
	Width        int
	Height       int
	WindowHandle uint64
	// Quality é a qualidade JPEG (1-100). 0 = padrão (80).
	Quality int
	// MaxDimension limita o maior lado da imagem final (0 = padrão 1600).
	// Reduz custo de visão/tokens e o tamanho do payload enviado à API.
	MaxDimension int
}

// CaptureResult é o resultado de uma captura, com os bytes já codificados.
type CaptureResult struct {
	Mode        string      `json:"mode"`
	MIME        string      `json:"mime"`
	Data        []byte      `json:"-"`
	Width       int         `json:"width"`
	Height      int         `json:"height"`
	OriginX     int         `json:"originX"`
	OriginY     int         `json:"originY"`
	Window      *WindowInfo `json:"window,omitempty"`
	Monitor     int         `json:"monitor"`
	Interactive bool        `json:"interactive,omitempty"`
	CapturedAt  time.Time   `json:"capturedAt"`

	// Thumbnail é uma versão reduzida (JPEG) usada para mostrar no chat o que a
	// IA capturou. Nunca entra no payload multimodal (só a imagem cheia).
	Thumbnail     []byte `json:"-"`
	ThumbnailMIME string `json:"-"`
}

// ThumbnailDataURL retorna a miniatura como data URL (vazia se não houver).
func (r *CaptureResult) ThumbnailDataURL() string {
	if r == nil || len(r.Thumbnail) == 0 {
		return ""
	}
	mime := r.ThumbnailMIME
	if mime == "" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(r.Thumbnail)
}

// DataURL retorna a imagem como data URL (usada pelo <img> do frontend e pelo
// painel de chat).
func (r *CaptureResult) DataURL() string {
	if r == nil || len(r.Data) == 0 {
		return ""
	}
	return "data:" + r.MIME + ";base64," + base64.StdEncoding.EncodeToString(r.Data)
}

// Base64 retorna apenas o payload base64 (sem prefixo data:).
func (r *CaptureResult) Base64() string {
	if r == nil || len(r.Data) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(r.Data)
}

// OverlayPayload é o estado entregue ao frontend para desenhar o overlay de
// seleção (tela congelada + lista de janelas para destacar).
type OverlayPayload struct {
	Session string `json:"session"`
	// At é o instante de criação do payload — o frontend descarta eventos
	// atrasados (broker do chat entrega pendências no próximo poll).
	At             time.Time     `json:"at"`
	Reason         string        `json:"reason,omitempty"`
	VirtualX       int           `json:"virtualX"`
	VirtualY       int           `json:"virtualY"`
	VirtualWidth   int           `json:"virtualWidth"`
	VirtualHeight  int           `json:"virtualHeight"`
	ImageWidth     int           `json:"imageWidth"`
	ImageHeight    int           `json:"imageHeight"`
	ImageDataURL   string        `json:"imageDataUrl"`
	Monitors       []MonitorInfo `json:"monitors"`
	Windows        []WindowInfo  `json:"windows"`
	RequestedByLLM bool          `json:"requestedByLlm"`
	// Flags da política local para a UI não oferecer ações proibidas.
	PolicyFullScreenAllowed bool `json:"policyFullScreenAllowed"`
	PolicyWindowRequired    bool `json:"policyWindowRequired"`
}

// Selection é a escolha do usuário no overlay de captura.
type Selection struct {
	Kind         string `json:"kind"` // "region" ou "window"
	X            int    `json:"x"`
	Y            int    `json:"y"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	WindowHandle uint64 `json:"windowHandle"`
	MonitorIndex int    `json:"monitorIndex"`
	// AnnotatedDataURL é a imagem final já com as anotações (setas, caixas,
	// texto) desenhadas pelo usuário no overlay. Quando presente, é ela que vale
	// — o backend apenas normaliza/reencoda.
	AnnotatedDataURL string `json:"annotatedDataUrl,omitempty"`
}

// ToolPayload monta o contrato JSON devolvido ao LLM como resultado da tool.
//
// Contrato de visão (consumido pelo DiscoveryRMM_API): quando "image_base64"
// está presente, o servidor anexa a imagem como parte multimodal da mensagem
// enviada ao provedor LLM. "note" é o resumo textual usado por modelos sem
// visão.
func (r *CaptureResult) ToolPayload(note string, target map[string]any) map[string]any {
	payload := map[string]any{
		"ok":           true,
		"type":         "screenshot",
		"mode":         r.Mode,
		"mime":         r.MIME,
		"image_base64": r.Base64(),
		"width":        r.Width,
		"height":       r.Height,
		"bytes":        len(r.Data),
		"captured_at":  r.CapturedAt.UTC().Format(time.RFC3339),
		"note":         note,
	}
	if target != nil {
		payload["target"] = target
	}
	if r.Window != nil {
		payload["window"] = r.Window
	}
	return payload
}

// Describe monta uma descrição textual curta (usada em logs e no modo sem visão).
func (r *CaptureResult) Describe() string {
	if r == nil {
		return "captura vazia"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "captura %s %dx%d (%s, %d bytes)", r.Mode, r.Width, r.Height, r.MIME, len(r.Data))
	if r.Window != nil {
		fmt.Fprintf(&sb, " da janela %q (%s, pid %d)", r.Window.Title, r.Window.ProcessName, r.Window.PID)
	}
	return sb.String()
}
