package remotedebug

// Config é a visão mínima da configuração de conexão usada pelo remote debug.
// Segue o padrão dos demais domínios (chat/notifications/psadt) que definem
// suas próprias views mínimas em vez de depender do tipo completo do package app.
type Config struct {
	AuthToken    string
	AgentID      string
	NatsServer   string
	NatsWsServer string
	// ApiServer é o servidor da API (host, com ou sem esquema/path — ex.
	// "discovery.pstefano.com.br" ou "https://host/api/"). É a ÚNICA fonte
	// confiável para derivar os endpoints NATS quando a config/comando não os
	// trazem: em deployments em que o broker só é exposto por WebSocket na 443,
	// NatsWsServer chega vazio e o comando remotedebug não carrega natsWsUrl.
	ApiServer string
}
