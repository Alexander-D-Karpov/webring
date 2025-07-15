package user

import (
	"context"

	"webring/internal/models"
)

type contextKey string

const userContextKey contextKey = "user"

func SetUserContext(ctx context.Context, user *models.User) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

func GetUserFromContext(ctx context.Context) *models.User {
	if user, ok := ctx.Value(userContextKey).(*models.User); ok {
		return user
	}
	return nil
}
