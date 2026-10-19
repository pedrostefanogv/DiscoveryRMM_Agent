package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeTestImages monta data URLs de imagem distintas para os testes.
func makeTestImages(n int) []string {
	images := make([]string, 0, n)
	for i := 0; i < n; i++ {
		images = append(images, fmt.Sprintf("data:image/png;base64,AAAA%d", i))
	}
	return images
}

// TestSetPendingImagesKeepsAll reproduz o bug do limite antigo de 3: o
// SetPendingImages truncava a lista e as imagens 4+ nunca chegavam ao LLM.
func TestSetPendingImagesKeepsAll(t *testing.T) {
	s := NewService(nil)
	images := makeTestImages(5)

	s.SetPendingImages(images)

	// O pending deve manter as 5 (takePendingImages devolve todas).
	taken := s.takePendingImages()
	if len(taken) != 5 {
		t.Fatalf("takePendingImages devolveu %d imagens, esperado 5", len(taken))
	}
	for i := range images {
		if taken[i] != images[i] {
			t.Errorf("imagem %d = %q, esperado %q", i, taken[i], images[i])
		}
	}

	// O cache do último turno sobrevive ao consumo.
	last := s.LastTurnImages()
	if len(last) != 5 {
		t.Fatalf("LastTurnImages devolveu %d imagens após o consumo, esperado 5", len(last))
	}

	// Novo SetPendingImages substitui o pending (sem acúmulo).
	s.SetPendingImages(makeTestImages(2))
	if got := len(s.takePendingImages()); got != 2 {
		t.Fatalf("segundo SetPendingImages: pending tem %d imagens, esperado 2", got)
	}

	// LastTurnImages devolve cópia: alterar o retorno não corrompe o cache.
	last = s.LastTurnImages()
	last[0] = "corrompido"
	if s.LastTurnImages()[0] == "corrompido" {
		t.Fatal("LastTurnImages expôs o slice interno (alteração vazou para o cache)")
	}
}

// TestLastTurnImagesPersistsWhenNoPendingImages garante que um turno posterior
// sem anexos não apaga o cache do último turno que teve imagens.
func TestLastTurnImagesPersistsWhenNoPendingImages(t *testing.T) {
	s := NewService(nil)
	s.SetPendingImages(makeTestImages(4))
	if got := len(s.takePendingImages()); got != 4 {
		t.Fatalf("takePendingImages devolveu %d, esperado 4", got)
	}
	if got := s.takePendingImages(); got != nil {
		t.Fatalf("takePendingImages sem pendências devolveu %d imagens, esperado nil", len(got))
	}
	if got := len(s.LastTurnImages()); got != 4 {
		t.Fatalf("LastTurnImages após turno sem anexos = %d, esperado 4", got)
	}
}

// TestMultiRoundSendsAllPendingImages verifica ponta a ponta que o turno
// multi-round envia TODAS as 5 imagens anexadas no request ao servidor.
func TestMultiRoundSendsAllPendingImages(t *testing.T) {
	var received agentStreamRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("falha ao decodificar request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"done\",\"content\":\"ok\",\"sessionId\":\"sess-test\"}\n\n")
	}))
	defer server.Close()

	s := NewService(nil)
	s.SetConfig(Config{
		Endpoint: server.URL,
		APIKey:   "mdz_test-token",
		AgentID:  "11111111-1111-1111-1111-111111111111",
	})
	images := makeTestImages(5)
	s.SetPendingImages(images)

	if _, err := s.SendStreamMultiRound(context.Background(), "oi", nil, nil, nil); err != nil {
		t.Fatalf("SendStreamMultiRound retornou erro: %v", err)
	}

	if len(received.Images) != 5 {
		t.Fatalf("request ao servidor levou %d imagens, esperado 5", len(received.Images))
	}
	for i := range images {
		if received.Images[i] != images[i] {
			t.Errorf("imagem %d enviada = %q, esperado %q", i, received.Images[i], images[i])
		}
	}

	// Após o turno, o cache do último turno tem as 5 imagens.
	last := s.LastTurnImages()
	if len(last) != 5 {
		t.Fatalf("LastTurnImages após o turno = %d, esperado 5", len(last))
	}
}
