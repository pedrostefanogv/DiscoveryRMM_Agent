package service

import (
	"context"
	"strings"

	"discovery/internal/database"
)

// actionUserContextKey é o tipo de chave privado para valores de contexto de ação.
// Usar um tipo privado evita colisões com outras chaves de contexto.
type actionUserContextKey struct{}

// ActionUserContext guarda as informações de identidade do usuário que originou
// a ação, propagadas via context.Context para o worker de execução.
type ActionUserContext struct {
	// UserSID é o Security Identifier do usuário (ex: S-1-5-21-...).
	UserSID string
	// UserName é o nome legível do usuário (ex: DESKTOP\pedro).
	UserName string
}

// withActionUserContext retorna um context enriquecido com a identidade do
// usuário que originou a ação. O worker deve chamar esta função antes de
// despachar a execução da ação.
func withActionUserContext(ctx context.Context, entry database.ActionQueueEntry) context.Context {
	userSID := strings.TrimSpace(entry.UserSID)
	userName := strings.TrimSpace(entry.UserName)
	if userSID == "" && userName == "" {
		return ctx
	}
	return context.WithValue(ctx, actionUserContextKey{}, ActionUserContext{
		UserSID:  userSID,
		UserName: userName,
	})
}

// actionUserContextFrom extrai as informações de identidade do usuário do
// contexto. Retorna zero-value e false se nenhum contexto de usuário existir.
func actionUserContextFrom(ctx context.Context) (ActionUserContext, bool) {
	v, ok := ctx.Value(actionUserContextKey{}).(ActionUserContext)
	return v, ok
}
