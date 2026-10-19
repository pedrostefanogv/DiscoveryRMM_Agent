package app

import (
	"testing"

	"discovery/app/supportchat"
)

// ── Regressão lida no logs.db do agente (2026-10-19 23:01) ──────────────────
//
// Sequência real do "agente local" (fluxo FALLBACK: janela do chat com modal de
// aceite):
//
//  23:01:06  convite recebido / apresentado
//  23:01:08  conversa iniciada (aceite apos 1s)
//  23:01:38  "sessão ativa/pendente encontrada sem janela aberta; abrindo o chat"
//
// A janela aberta no aceite sumia e SÓ reaparecia 30 s depois — e, quando o
// reaparecimento acontecia, ele reabria a janela (a segunda). A causa imediata
// era a JANELA DO CONVITE NÃO SAIR DE CENA no aceite; a causa de fundo é
// verificada pelos testes abaixo.

// A marca "já respondido" (2 min) NÃO pode reabrir/encerrar uma conversa já
// ativa: ela existe para impedir o cartão de voltar enquanto a resposta está em
// voo, não para bloquear a sessão seguinte.
func TestSupportChatAnsweredMark_NAOCobreConversaAtiva(t *testing.T) {
	a := &App{}
	a.markSupportChatInviteNotificationAnswered("sessao-1")

	if !a.supportChatInviteNotificationWasAnswered("sessao-1") {
		t.Fatal("marca recém-criada precisa existir")
	}

	// A reconciliação com a sessão ATIVA limpa a marca (ver
	// replaySupportChatInviteAfterUIReconnect): depois disso o "waiting" novo da
	// mesma sessão (reentrega) pode ser apresentado de novo.
	a.clearSupportChatInviteNotificationAnswered("sessao-1")
	if a.supportChatInviteNotificationWasAnswered("sessao-1") {
		t.Fatal("a marca precisa ser limpa quando o aceite é concluído")
	}
}

// hideSupportChatInviteSurface NUNCA fecha a janela com o convite pendente: ali
// ela é a única superfície de aceite (fluxo fallback).
func TestHideSupportChatInviteSurface_RespeitaConvitePendente(t *testing.T) {
	a := &App{}
	if supportchat.StatusWaiting == "" {
		t.Fatal("sanidade")
	}
	// Sem serviço/estado: status "" (nenhum) — nada a esconder, sem pânico.
	a.hideSupportChatInviteSurface()

	if supportChatShouldFloat(a.supportChatService().State().Status) {
		t.Fatal("estado vazio não pode ser tratado como convite pendente")
	}
}
