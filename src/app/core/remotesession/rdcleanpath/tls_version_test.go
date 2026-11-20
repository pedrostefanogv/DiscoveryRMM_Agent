package rdcleanpath

import (
	"os"
	"strings"
	"testing"
)

// TestHandshakeCapTLS12 garante que o handshake com o TermService NUNCA
// negocia TLS 1.3.
//
// MOTIVO (causa raiz do WebRDP "tela preta" em producao):
// O CredSSP/NLA nao funciona sobre TLS 1.3. Nessa versao o certificado do
// servidor e enviado CIFRADO no handshake, e o channel binding do CredSSP
// ("tls-server-end-point") depende do certificado visivel. O Schannel do
// Windows espera CredSSP sobre TLS 1.2.
//
// Sintoma real observado: o handshake TLS COMPLETAVA, o agent registrava
// "handshake ok; iniciando relay", e ~100 ms depois o TermService abortava com
// "remote error: tls: internal error" ao receber o token CredSSP. O viewer
// retentava em loop e o canvas ficava preto.
//
// Comprovado na maquina alvo: o TermService negocia Tls13 por padrao e aceita
// Tls12 quando o cliente o limita.
func TestHandshakeCapTLS12(t *testing.T) {
	src, err := os.ReadFile("handshake.go")
	if err != nil {
		t.Fatalf("ler handshake.go: %v", err)
	}
	// Normaliza espacos: o gofmt ALINHA os campos do struct com multiplos
	// espacos, entao comparar com um unico espaco daria falso negativo.
	code := strings.Join(strings.Fields(string(src)), " ")

	if !strings.Contains(code, "MaxVersion: tls.VersionTLS12") {
		t.Error("handshake sem MaxVersion=TLS1.2: pode negociar TLS 1.3 e quebrar o " +
			"CredSSP/NLA com 'remote error: tls: internal error'")
	}
	// MinVersion continua 1.2 (nao aceitar TLS antigo).
	if !strings.Contains(code, "MinVersion: tls.VersionTLS12") {
		t.Error("MinVersion esperado em TLS 1.2")
	}
	// O comentario precisa explicar o porque, para ninguem remover o cap.
	if !strings.Contains(code, "CredSSP") || !strings.Contains(code, "TLS 1.3") {
		t.Error("falta o comentario explicando por que TLS 1.3 e proibido aqui")
	}
}
