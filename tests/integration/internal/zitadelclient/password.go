package zitadelclient

import (
	"context"
	"fmt"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	session_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/session/v2"
	user_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// CheckPassword proves that username can authenticate with password in
// the given org by opening a Zitadel session with a user + password check
// (session v2 CreateSession). It returns the checked user's ID. A password
// that Zitadel cannot verify (wrong, or imported in a hash format its
// verifier does not accept) fails here rather than at the first real
// login.
func CheckPassword(ctx context.Context, conn *zitadel.Connection, orgID, username, password string) (string, error) {
	users := user_pb.NewUserServiceClient(conn)
	list, err := users.ListUsers(ctx, &user_pb.ListUsersRequest{
		Queries: []*user_pb.SearchQuery{
			{Query: &user_pb.SearchQuery_OrganizationIdQuery{
				OrganizationIdQuery: &user_pb.OrganizationIdQuery{OrganizationId: orgID},
			}},
			{Query: &user_pb.SearchQuery_UserNameQuery{
				UserNameQuery: &user_pb.UserNameQuery{UserName: username},
			}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("list users %q in org %s: %w", username, orgID, err)
	}
	if len(list.GetResult()) != 1 {
		return "", fmt.Errorf("expected exactly one user %q in org %s, got %d", username, orgID, len(list.GetResult()))
	}
	userID := list.GetResult()[0].GetUserId()

	sessions := session_pb.NewSessionServiceClient(conn)
	resp, err := sessions.CreateSession(ctx, &session_pb.CreateSessionRequest{
		Checks: &session_pb.Checks{
			User:     &session_pb.CheckUser{Search: &session_pb.CheckUser_UserId{UserId: userID}},
			Password: &session_pb.CheckPassword{Password: password},
		},
	})
	if err != nil {
		return "", fmt.Errorf("password check for user %s: %w", userID, err)
	}
	if resp.GetSessionToken() == "" {
		return "", fmt.Errorf("password check for user %s: session created without a token", userID)
	}
	return userID, nil
}
