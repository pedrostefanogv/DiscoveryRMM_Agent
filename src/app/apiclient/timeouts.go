// Package apiclient encapsula a detecção de features da API e os timeouts
// centralizados dos endpoints da API v1, separados do App.
package apiclient

import "time"

// Timeouts centralizados de endpoints da API v1 para facilitar ajustes
// e consistência entre componentes.
const (
	// Registro & Bootstrap
	APIEndpointRegister      = 30 * time.Second
	APIEndpointConfiguration = 15 * time.Second
	APIEndpointSyncManifest  = 15 * time.Second

	// Inventário
	APIEndpointHardware = 20 * time.Second
	APIEndpointSoftware = 20 * time.Second

	// Automação
	APIEndpointPolicySync      = 45 * time.Second
	APIEndpointExecutionAck    = 30 * time.Second
	APIEndpointExecutionResult = 30 * time.Second
	APIEndpointCustomFields    = 30 * time.Second

	// P2P
	APIEndpointP2PSeedPlan  = 20 * time.Second
	APIEndpointP2PTelemetry = 20 * time.Second
	APIEndpointP2PBootstrap = 15 * time.Second

	// Provisioning & Zero-Touch
	APIEndpointProvisioningToken = 15 * time.Second

	// Updates
	APIEndpointUpdateManifest = 30 * time.Second
	APIEndpointUpdateDownload = 30 * time.Minute
	APIEndpointUpdateReport   = 30 * time.Second

	// Comandos REST
	APIEndpointCommands = 20 * time.Second

	// Tickets
	APIEndpointTicketsList    = 15 * time.Second
	APIEndpointTicketsCreate  = 15 * time.Second
	APIEndpointTicketsComment = 10 * time.Second
	APIEndpointTicketsClose   = 10 * time.Second

	// Chat AI
	APIEndpointChatStream = 130 * time.Second
	APIEndpointChatSync   = 120 * time.Second

	// Segurança
	APIEndpointTLsMismatch = 5 * time.Second

	// Decommission
	APIEndpointDecommission = 20 * time.Second

	// Genéricos
	APIEndpointKnowledgeBase = 15 * time.Second
	APIEndpointSupportTicket = 10 * time.Second

	// P2P local
	APIEndpointP2PLocalDownload   = 45 * time.Second
	APIEndpointP2PLocalOnboarding = 10 * time.Second
)
