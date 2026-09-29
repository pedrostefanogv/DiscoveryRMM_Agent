package support

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// list_tickets alimenta a verificação de duplicidade da IA: o resumo precisa
// colocar os chamados ABERTOS primeiro, sinalizar isOpen e informar quando o
// recorte ficou incompleto.
func TestCompactTicketList_OpenFirstAndFlags(t *testing.T) {
	closed := "2026-01-02T10:00:00Z"
	empty := "   "
	tickets := []APITicket{
		{ID: "closed-1", Title: "Antigo encerrado", ClosedAt: &closed},
		{ID: "open-1", Title: "Impressora sem imprimir", ClosedAt: nil},
		{ID: "open-2", Title: "Encerrado sem data", ClosedAt: &empty},
	}

	out := compactTicketList(tickets)

	if got := out["total"].(int); got != 3 {
		t.Fatalf("total esperado 3, obtido %d", got)
	}
	if got := out["openCount"].(int); got != 2 {
		t.Fatalf("openCount esperado 2 (ClosedAt nulo/vazio), obtido %d", got)
	}
	if out["truncated"].(bool) {
		t.Fatal("não deveria marcar truncado com 3 chamados")
	}

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal falhou: %v", err)
	}
	var payload struct {
		Tickets []struct {
			ID     string `json:"id"`
			IsOpen bool   `json:"isOpen"`
		} `json:"tickets"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal falhou: %v", err)
	}
	if len(payload.Tickets) != 3 {
		t.Fatalf("esperados 3 itens, obtidos %d", len(payload.Tickets))
	}
	if payload.Tickets[0].ID != "open-1" || !payload.Tickets[0].IsOpen {
		t.Fatalf("primeiro item deveria ser o aberto open-1, obtido %+v", payload.Tickets[0])
	}
	if payload.Tickets[1].ID != "open-2" || !payload.Tickets[1].IsOpen {
		t.Fatalf("segundo item deveria ser open-2 (data vazia = aberto), obtido %+v", payload.Tickets[1])
	}
	if payload.Tickets[2].ID != "closed-1" || payload.Tickets[2].IsOpen {
		t.Fatalf("último item deveria ser o encerrado closed-1, obtido %+v", payload.Tickets[2])
	}
}

func TestCompactTicketList_TruncatesAndReports(t *testing.T) {
	tickets := make([]APITicket, 0, 60)
	for i := 0; i < 60; i++ {
		tickets = append(tickets, APITicket{ID: fmt.Sprintf("t-%02d", i), Title: "chamado"})
	}

	out := compactTicketList(tickets)

	if got := out["returned"].(int); got != maxListedTickets {
		t.Fatalf("returned esperado %d, obtido %d", maxListedTickets, got)
	}
	if got := out["total"].(int); got != 60 {
		t.Fatalf("total esperado 60, obtido %d", got)
	}
	if !out["truncated"].(bool) {
		t.Fatal("deveria marcar truncated=true quando o recorte é menor que o total")
	}
}

func TestCompactTicketList_TrimsHugeDescription(t *testing.T) {
	long := strings.Repeat("x", ticketListDescriptionChars+500)
	out := compactTicketList([]APITicket{{ID: "a", Title: "t", Description: long}})

	summaries := out["tickets"].([]ticketListSummary)
	if len(summaries) != 1 {
		t.Fatalf("esperado 1 resumo, obtido %d", len(summaries))
	}
	if got := []rune(summaries[0].Description); len(got) != ticketListDescriptionChars+1 {
		t.Fatalf("descrição deveria ser cortada em %d runes + reticências, obtido %d", ticketListDescriptionChars, len(got))
	}
}

func TestCompactTicketList_Empty(t *testing.T) {
	out := compactTicketList(nil)
	if got := out["total"].(int); got != 0 {
		t.Fatalf("total esperado 0, obtido %d", got)
	}
	if out["truncated"].(bool) {
		t.Fatal("lista vazia não deveria ser truncada")
	}
}
