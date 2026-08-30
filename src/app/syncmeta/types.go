// Package syncmeta contém tipos puros (somente stdlib) do domínio
// Sync/offline. Nenhum tipo aqui importa o package app, permitindo que o
// núcleo do domínio (src/app/sync) e as bridges na raiz compartilhem os
// mesmos tipos sem ciclo de importação.
package syncmeta

import "time"

const (
	// SyncRevisionsCacheKey é a chave de cache persistido das revisões do
	// sync-manifest.
	SyncRevisionsCacheKey = "sync_manifest_revisions"

	// DefaultPollSeconds é o intervalo padrão de polling do sync-manifest.
	DefaultPollSeconds = 900

	// MinPollSeconds é o piso mínimo de segurança para o intervalo de polling
	// recomendado pelo servidor. Evita que um manifesto mal configurado cause
	// polling excessivo (ex.: recommendedPollSeconds=60 → 7200 chamadas/dia).
	MinPollSeconds = 300

	// ProcessedEventTTL é o TTL de eventos processados (deduplicação de ping).
	ProcessedEventTTL = 30 * time.Minute

	// NonCriticalBackoffMin/Max delimitam o backoff aleatório de tráfego
	// não essencial quando o servidor está sobrecarregado.
	NonCriticalBackoffMin = 10 * time.Minute
	NonCriticalBackoffMax = 15 * time.Minute

	// GlobalPongStaleAfter define quando um tenant.global.pong é considerado
	// obsoleto para o sinal de conectividade online.
	GlobalPongStaleAfter = 3 * time.Minute
)

// SyncManifestResource descreve um recurso do sync-manifest.
type SyncManifestResource struct {
	Resource                 string `json:"resource"`
	Variant                  string `json:"variant"`
	Revision                 string `json:"revision"`
	RecommendedSyncInSeconds int    `json:"recommendedSyncInSeconds"`
	Endpoint                 string `json:"endpoint"`
}

// SyncManifestResponse é a resposta do endpoint /api/v1/agent-auth/me/sync-manifest.
type SyncManifestResponse struct {
	GeneratedAtUTC         string                 `json:"generatedAtUtc"`
	RecommendedPollSeconds int                    `json:"recommendedPollSeconds"`
	MaxStaleSeconds        int                    `json:"maxStaleSeconds"`
	Resources              []SyncManifestResource `json:"resources"`
}

// SyncTrigger representa um gatilho de sincronização enfileirado.
type SyncTrigger struct {
	Resource string
	Variant  string
	Revision string
	Source   string
}

// CommandResultOutboxPayload é o payload persistido no outbox de resultados
// de comando.
type CommandResultOutboxPayload struct {
	DispatchID   string `json:"dispatchId,omitempty"`
	CommandID    string `json:"commandId"`
	ExitCode     int    `json:"exitCode"`
	Output       string `json:"output,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}
