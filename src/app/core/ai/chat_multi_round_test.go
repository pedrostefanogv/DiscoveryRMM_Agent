package ai

import (
	"testing"
	"time"
)

func TestRoundTimeout(t *testing.T) {
	tests := []struct {
		round    int
		expected time.Duration
	}{
		{0, 60 * time.Second},
		{1, 90 * time.Second},
		{2, 130 * time.Second},
		{3, 130 * time.Second},
		{99, 130 * time.Second},
	}
	for _, tt := range tests {
		if got := roundTimeout(tt.round); got != tt.expected {
			t.Errorf("roundTimeout(%d) = %v, esperado %v", tt.round, got, tt.expected)
		}
	}
}

func TestDetectIncompleteResponse(t *testing.T) {
	tests := []struct {
		name      string
		assistant string
		want      bool
	}{
		// Promessas de ação sem conclusão → devem disparar
		{"promessa vou abrir", "Vou abrir o chamado para você agora.", true},
		{"promessa so um instante", "Só um instante enquanto registro as informações.", true},
		{"promessa deixa eu", "Deixa eu verificar os chamados da sua máquina.", true},
		{"promessa vou verificar", "Vou verificar isso para você.", true},
		{"promessa aguarde", "Aguarde um momento, por favor.", true},

		// Respostas concluídas → não devem disparar
		{"conclusao pronto", "Pronto! O chamado foi criado com sucesso.", false},
		{"conclusao feito", "Feito! Desinstalei o programa solicitado.", false},
		{"conclusao instalado", "O pacote foi instalado com sucesso.", false},
		{"conclusao resolvido", "Problema resolvido! Era só reiniciar o serviço.", false},
		{"conclusao tudo certo", "Tudo certo por aqui. Posso ajudar em mais alguma coisa?", false},

		// Resposta longa (> 300 chars) → tratada como completa, mesmo com "vou verificar"
		{"resposta longa com promessa", "Vou verificar o consumo de memória do seu computador. " +
			"Analisando os processos em execução, notei que o Firefox está consumindo cerca de 2 GB " +
			"de memória RAM, o qBittorrent outros 870 MB e o Phone Link aproximadamente 586 MB. " +
			"Além disso, o Explorer e o Discord juntos somam quase 1 GB. Para resolver a lentidão, " +
			"recomendo fechar o qBittorrent e reduzir o número de abas do Firefox, o que deve liberar " +
			"memória suficiente para o sistema voltar a responder normalmente.", false},

		// Vazio ou sem marcador
		{"vazio", "", false},
		{"sem marcador", "Seu computador está funcionando normalmente.", false},
		{"pergunta direta", "Qual programa você gostaria de instalar?", false},
	}
	for _, tt := range tests {
		if got := detectIncompleteResponse(tt.assistant); got != tt.want {
			t.Errorf("%s: detectIncompleteResponse(%q) = %v, esperado %v", tt.name, tt.assistant, got, tt.want)
		}
	}
}

func TestPatternsMatch(t *testing.T) {
	tests := []struct {
		msg      string
		keywords []string
		want     bool
	}{
		{"abra um chamado por favor", []string{"abra", "chamado"}, true},
		{"tem algum chamado aberto para minha maquina", []string{"chamado", "aberto", "minha", "maquina", "tem"}, true},
		{"meus chamados", []string{"meus", "chamados"}, true},
		{"quero instalar um programa", []string{"abra", "chamado"}, false},
		{"", []string{"abra", "chamado"}, false},
		{"chamado", []string{"abra", "chamado"}, false},
	}
	for _, tt := range tests {
		if got := patternsMatch(tt.msg, tt.keywords); got != tt.want {
			t.Errorf("patternsMatch(%q, %v) = %v, esperado %v", tt.msg, tt.keywords, got, tt.want)
		}
	}
}

func TestHasConfirmedAction(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"abra", true},
		{"pode abrir", true},
		{"sim", true},
		{"prossiga", true},
		{"ok", true},
		{"confirma", true},
		{"Abra o chamado", true},
		// Mensagens longas não são confirmações curtas
		{"Pode abrir um chamado para verificar se tem virus no meu computador", false},
		{"quero saber o status do meu computador", false},
	}
	for _, tt := range tests {
		if got := hasConfirmedAction(tt.msg); got != tt.want {
			t.Errorf("hasConfirmedAction(%q) = %v, esperado %v", tt.msg, got, tt.want)
		}
	}
}
