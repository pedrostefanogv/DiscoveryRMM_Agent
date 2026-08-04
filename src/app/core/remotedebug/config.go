package remotedebug

// Config é a visão mínima da configuração de conexão usada pelo remote debug.
// Segue o padrão dos demais domínios (chat/notifications/psadt) que definem
// suas próprias views mínimas em vez de depender do tipo completo do package app.
type Config struct {
	AuthToken    string
	AgentID      string
	NatsServer   string
	NatsWsServer string
}
