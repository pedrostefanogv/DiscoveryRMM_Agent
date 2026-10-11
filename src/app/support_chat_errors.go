package app

import "errors"

// Erros do chat de suporte expostos às bindings da janela.
var (
	errSupportChatInvalidAttachment = errors.New("anexo inválido: nome, tipo e conteúdo são obrigatórios")
	errSupportChatInvalidInvite     = errors.New("convite de chat inválido: sessionId obrigatório")
	errSupportChatBusy              = errors.New("já existe um chat de suporte ativo nesta máquina")
)
