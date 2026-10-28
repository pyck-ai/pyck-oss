package zitadelclient

import (
	"context"
	"fmt"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	user_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
)

// UserIDByUsername returns the Zitadel id of the org's user carrying the
// given username. Exactly one match is required: a username is unique within
// an organization, so anything else means the caller named a user that does
// not exist there, or that the query lost its org filter.
//
// Callers address users by username because that is what a fixture, an
// operator or a test knows; Zitadel ids are minted per user and never
// reported back out of the flows that create them.
func UserIDByUsername(ctx context.Context, conn *zitadel.Connection, orgID, username string) (string, error) {
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
	return list.GetResult()[0].GetUserId(), nil
}
