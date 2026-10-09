// Package debughttp encapsula o servidor HTTP de debug (frontend + REST bridge
// + SSE de chat), separado do App.
package debughttp

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
)

// ChatEventBroker gerencia inscritos SSE para eventos de streaming do chat.
// Permite que o chat funcione no navegador (debug HTTP) onde o runtime Wails
// não está disponível para receber EventsEmit/EventsOn.
//
// Além dos inscritos SSE (canais), mantém um buffer de polling para o webview
// nativo Wails v3, já que EventSource é bloqueado por mixed-content
// (https://wails.localhost → http://127.0.0.1) e eventos nativos
// (Events.On) não são confiáveis para streaming de alta frequência.
type ChatEventBroker struct {
	mu          sync.RWMutex
	subscribers map[chan string]struct{}
	pollBuffer  []string // buffer circular para PollChatEvents (binding Wails)
	pollMax     int      // limite máximo de eventos no buffer
	pollWrite   int      // índice de escrita (circular)
	// B4: descartes separados POR CAUSA (antes um único contador somava as duas
	// e o diagnóstico não dizia se o problema era inscrito SSE lento ou polling
	// atrasado — causas e correções diferentes).
	subscriberDropped int64 // canal SSE cheio (inscrito lento)
	pollOverwritten   int64 // buffer de polling estourado (sobrescrita circular)
}

// NewChatEventBroker cria um broker de eventos de chat.
func NewChatEventBroker() *ChatEventBroker {
	return &ChatEventBroker{
		subscribers: make(map[chan string]struct{}),
		pollMax:     512,
	}
}

// Subscribe registra um novo canal de eventos.
func (b *ChatEventBroker) Subscribe() chan string {
	ch := make(chan string, 128)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe remove um canal de eventos.
func (b *ChatEventBroker) Unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
}

// Publish envia um evento JSON para todos os inscritos SSE e adiciona ao
// buffer de polling.
// Formato: {"event":"chat:token","data":"conteudo"}
func (b *ChatEventBroker) Publish(eventType, data string) {
	payload, err := json.Marshal(map[string]string{
		"event": eventType,
		"data":  data,
	})
	if err != nil {
		return
	}
	line := string(payload)

	b.mu.RLock()
	// Enviar para inscritos SSE
	for ch := range b.subscribers {
		select {
		case ch <- line:
		default:
			// descarta se inscrito estiver lento (buffer cheio)
			// B5: contabiliza a queda — antes a métrica ficava sempre em 0.
			atomic.AddInt64(&b.subscriberDropped, 1)
		}
	}
	b.mu.RUnlock()

	// Adicionar ao buffer de polling circular (sempre, mesmo sem inscritos SSE).
	// Buffer circular: nunca bloqueia — eventos antigos são sobrescritos se o
	// polling estiver muito lento. Isto garante que chat:done SEMPRE seja
	// preservado (últimos N eventos), evitando polling infinito.
	b.mu.Lock()
	if len(b.pollBuffer) < b.pollMax {
		b.pollBuffer = append(b.pollBuffer, line)
	} else {
		// Buffer circular: sobrescreve a posição mais antiga.
		// B4 (revisão 2026-10-08): a sobrescrita TAMBÉM é um evento perdido e
		// antes não era contada — a métrica ficava em 0 mesmo com o polling
		// atrasado (e o evento descartado costuma ser justamente o createSurface
		// /updateDataModel, o começo do card).
		b.pollBuffer[b.pollWrite] = line
		b.pollWrite = (b.pollWrite + 1) % b.pollMax
		atomic.AddInt64(&b.pollOverwritten, 1)
	}
	b.mu.Unlock()
}

// DrainPollBuffer retorna todos os eventos pendentes em ordem e esvazia o
// buffer. Usado pelo PollChatEvents (binding Wails) para o webview nativo.
// Garante que eventos terminais (chat:done/error/stopped) não sejam perdidos
// quando o buffer circular é usado.
func (b *ChatEventBroker) DrainPollBuffer() []string {
	b.mu.Lock()
	n := len(b.pollBuffer)
	if n == 0 {
		b.mu.Unlock()
		return nil
	}

	var events []string
	if n < b.pollMax {
		// Buffer ainda em crescimento linear — retorna em ordem e limpa
		events = b.pollBuffer
		b.pollBuffer = nil
	} else {
		// Buffer circular cheio — retorna em ordem cronológica (write → write+n)
		events = make([]string, n)
		for i := 0; i < n; i++ {
			events[i] = b.pollBuffer[(b.pollWrite+i)%b.pollMax]
		}
		b.pollBuffer = nil
		b.pollWrite = 0
	}
	b.mu.Unlock()
	return events
}

// DrainDrops devolve (e zera) os descartes separados por causa:
//   - subscriberDropped: inscrito SSE não consumiu o canal a tempo;
//   - pollOverwritten:   o buffer circular de polling foi sobrescrito.
//
// Separar as causas importa para o diagnóstico: inscrito lento aponta para o
// consumidor SSE (navegador/debug), enquanto sobrescrita aponta para o polling
// do WebView não acompanhar o stream.
func (b *ChatEventBroker) DrainDrops() (subscriberDropped, pollOverwritten int64) {
	return atomic.SwapInt64(&b.subscriberDropped, 0), atomic.SwapInt64(&b.pollOverwritten, 0)
}

// FrontendFS guarda o filesystem do frontend embedado para o servidor HTTP.
var (
	frontendFS   http.FileSystem
	frontendFSMu sync.RWMutex
)

// SetFrontendAssets armazena o filesystem do frontend embedado.
// O embed.FS de `//go:embed all:frontend` tem prefixo `frontend/` — removido
// via fs.Sub para servir paths como `index.html`, `app.js`, etc.
func SetFrontendAssets(fs http.FileSystem) {
	frontendFSMu.Lock()
	frontendFS = fs
	frontendFSMu.Unlock()
}

// GetFrontendFS retorna o filesystem do frontend, ou nil se não configurado.
func GetFrontendFS() http.FileSystem {
	frontendFSMu.RLock()
	defer frontendFSMu.RUnlock()
	return frontendFS
}
