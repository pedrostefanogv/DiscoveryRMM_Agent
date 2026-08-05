// Package debughttp encapsula o servidor HTTP de debug (frontend + REST bridge
// + SSE de chat), separado do App.
package debughttp

import (
	"encoding/json"
	"net/http"
	"sync"
)

// ChatEventBroker gerencia inscritos SSE para eventos de streaming do chat.
// Permite que o chat funcione no navegador (debug HTTP) onde o runtime Wails
// não está disponível para receber EventsEmit/EventsOn.
type ChatEventBroker struct {
	mu          sync.RWMutex
	subscribers map[chan string]struct{}
}

// NewChatEventBroker cria um broker de eventos de chat.
func NewChatEventBroker() *ChatEventBroker {
	return &ChatEventBroker{
		subscribers: make(map[chan string]struct{}),
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

// Publish envia um evento JSON para todos os inscritos SSE.
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
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- line:
		default:
			// descarta se inscrito estiver lento (buffer cheio)
		}
	}
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
