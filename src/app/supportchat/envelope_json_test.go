package supportchat

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEnvelopeJSON_NaoEmiteCamposVazios reproduz o bug que impedia as mensagens
// do usuário final de chegarem ao console do suporte.
//
// O contrato do SERVIDOR (Discovery.Core.DTOs.TicketChatEnvelope) declara
// TicketId como Guid? e AgentId como Guid. O agent, ao serializar o envelope de
// uma conversa AVULSA (sem chamado), emitia "ticketId":"" e "agentId":"" — string
// vazia não é Guid válido e o System.Text.Json lançava
//
//	The JSON value could not be converted to System.Nullable`1[System.Guid]. Path: $.ticketId
//
// O envelope era descartado na ingestão e a mensagem nunca era persistida: o
// operador não via nada no navegador, embora o agent tivesse recebido PubAck do
// JetStream (o envio "funcionava" do lado da máquina).
//
// O teste FALHA no formato antigo (tags sem omitempty) e passa com a correção.
func TestEnvelopeJSON_NaoEmiteCamposVazios(t *testing.T) {
	message := Envelope{
		EventType:       EventMessage,
		SessionID:       "b066d11d-84dc-401f-ba23-ba907fb5c5d4",
		AuthorKind:      "enduser",
		Body:            "Teste do chat",
		ClientMessageID: "0d1b6a5f-9d3a-4a2e-9d55-1c2f9a2f7b41",
	}

	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	raw := string(payload)
	if strings.Contains(raw, `"ticketId"`) {
		t.Fatalf("envelope de conversa avulsa não pode emitir ticketId (o servidor recusa string vazia como Guid?): %s", raw)
	}
	if strings.Contains(raw, `"agentId"`) {
		t.Fatalf("envelope de conversa avulsa não pode emitir agentId (o servidor recusa string vazia como Guid; o id vem do subject): %s", raw)
	}

	// O resto do contrato precisa continuar presente.
	for _, field := range []string{`"eventType"`, `"sessionId"`, `"authorKind"`, `"body"`, `"clientMessageId"`} {
		if !strings.Contains(raw, field) {
			t.Fatalf("campo %s ausente no envelope: %s", field, raw)
		}
	}
}

// TestEnvelopeJSON_MantemIdentidadeQuandoExiste garante que a correção não
// silenciou os campos quando eles TÊM valor: conversa vinculada a chamado
// continua levando ticketId, e o agent standalone continua levando agentId.
func TestEnvelopeJSON_MantemIdentidadeQuandoExiste(t *testing.T) {
	payload, err := json.Marshal(Envelope{
		EventType: EventMessage,
		SessionID: testSessionID,
		TicketID:  testTicketID,
		AgentID:   testAgentID,
		Body:      "oi",
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	raw := string(payload)
	if !strings.Contains(raw, `"ticketId":"`+testTicketID+`"`) {
		t.Fatalf("ticketId deveria estar presente quando há chamado: %s", raw)
	}
	if !strings.Contains(raw, `"agentId":"`+testAgentID+`"`) {
		t.Fatalf("agentId deveria estar presente quando informado: %s", raw)
	}
}

// TestEnvelopeJSON_ContratoDoServidorAceitaEnvelopeDeConversaAvulsa valida que o
// envelope publicado pelo agent desserializa no MESMO contrato de campos do
// servidor (camelCase, sem campos obrigatórios faltando). É a guarda que faltava:
// os testes existentes só exercitavam envelopes COM ticketId.
func TestEnvelopeJSON_ContratoDoServidorAceitaEnvelopeDeConversaAvulsa(t *testing.T) {
	payload, err := json.Marshal(Envelope{
		EventType:       EventMessage,
		SessionID:       testSessionID,
		AuthorKind:      "enduser",
		Body:            "Teste do chat",
		ClientMessageID: testAgentID,
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Espelho mínimo do TicketChatEnvelope do servidor.
	var server struct {
		EventType       string  `json:"eventType"`
		SessionID       string  `json:"sessionId"`
		TicketID        *string `json:"ticketId"`
		AgentID         *string `json:"agentId"`
		AuthorKind      string  `json:"authorKind"`
		Body            string  `json:"body"`
		ClientMessageID string  `json:"clientMessageId"`
	}
	if err := json.Unmarshal(payload, &server); err != nil {
		t.Fatalf("envelope do agent não desserializa no contrato do servidor: %v (payload=%s)", err, payload)
	}

	if server.TicketID != nil || server.AgentID != nil {
		t.Fatalf("campos de identidade deveriam vir ausentes (nil) numa conversa avulsa: payload=%s", payload)
	}
	if server.SessionID != testSessionID || server.Body != "Teste do chat" {
		t.Fatalf("envelope perdeu conteúdo: %+v", server)
	}
}
