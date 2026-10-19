//go:build !windows

package app

import (
	"context"

	"discovery/app/supportchat"
)

// startSupportChatInviteNotification não existe fora do Windows: o convite
// continua usando a janela do chat com o modal (fallback mantido em
// presentSupportChatInvite).
func startSupportChatInviteNotification(ctx context.Context, invite supportchat.Invite) (<-chan supportChatInviteNotificationResult, func(), bool) {
	return nil, nil, false
}
