package app

// Testes do contrato IPC serviço↔UI (PLANO_AGENT_SERVICE_SYSTEM.md, Fase 5).

import (
	"bufio"
	"strings"
	"testing"
)

func TestIPCMessageRoundTrip(t *testing.T) {
	msg := NewIPCMessage(IPCMsgEvent, map[string]any{"kind": "connectivity", "connected": true})
	data := EncodeIPCMessage(msg)
	if data == nil {
		t.Fatal("EncodeIPCMessage retornou nil")
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("mensagem codificada deve terminar com \\n (JSON lines)")
	}

	decoded, err := DecodeIPCMessage(bufio.NewReader(strings.NewReader(string(data))))
	if err != nil {
		t.Fatalf("DecodeIPCMessage: %v", err)
	}
	if decoded.Type != IPCMsgEvent {
		t.Fatalf("tipo esperado %q, got %q", IPCMsgEvent, decoded.Type)
	}
	if decoded.Payload["kind"] != "connectivity" {
		t.Fatalf("payload kind esperado connectivity, got %v", decoded.Payload["kind"])
	}
}

func TestDecodeIPCMessageRejectsInvalid(t *testing.T) {
	if _, err := DecodeIPCMessage(bufio.NewReader(strings.NewReader("nao-json\n"))); err == nil {
		t.Fatal("esperado erro para json inválido")
	}
	if _, err := DecodeIPCMessage(bufio.NewReader(strings.NewReader("\n"))); err == nil {
		t.Fatal("esperado erro para linha vazia")
	}
}

func TestIPCContractConstants(t *testing.T) {
	// Garante o contrato mínimo do plano (Fase 2).
	for _, mt := range []IPCMessageType{
		IPCMsgHello, IPCMsgHelloAck, IPCMsgStatus, IPCMsgEvent,
		IPCMsgNotificationRespond, IPCMsgCommandResult,
	} {
		if mt == "" {
			t.Fatal("tipo de mensagem vazio no contrato")
		}
	}
	// Os DOIS canais precisam de nome válido e distinto. O pipe admin nasceu
	// malformado na 1.2.1 (faltava o prefixo) e derrubou TODAS as mutações com
	// "Incorrect function" no ListenPipe — ver ipc_pipe_name_test.go.
	for _, name := range []string{IPCPipeName, IPCPipeAdminName} {
		if err := validateIPCPipeName(name); err != nil {
			t.Fatalf("pipe name inválido: %v", err)
		}
	}
	if IPCPipeName == IPCPipeAdminName {
		t.Fatal("pipe de leitura e pipe admin não podem usar o mesmo nome")
	}
}

// TestServiceModeFlagZeroValue garante que o modo padrão (UI standalone)
// não ativa ServiceMode por acidente.
func TestServiceModeFlagZeroValue(t *testing.T) {
	var opts AppStartupOptions
	if opts.ServiceMode {
		t.Fatal("ServiceMode deve ser false por padrão")
	}
}
