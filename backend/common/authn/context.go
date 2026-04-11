package authn

import (
	"context"
)

type contextKeyUser struct{}

// Context adds a User to the context.
func Context(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, contextKeyUser{}, user)
}

// ForContext retrieves the User from the context.
// Returns an empty User if not found.
func ForContext(ctx context.Context) User {
	user, ok := ctx.Value(contextKeyUser{}).(*User)
	if ok && user != nil {
		return *user
	}

	return User{}
}
