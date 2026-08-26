package zitadelclient

import (
	"context"
	"fmt"
	"time"

	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	user_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
)

// patExpiry sets PATs to one year out, matching the convention in
// myworkflow/cmd/provision and pyck's bootstrap.
const patExpiry = 365 * 24 * time.Hour

// EnsureMachineUser looks up (or creates) a machine user in the given
// Zitadel org and returns its ID. Idempotent on the username.
func EnsureMachineUser(ctx context.Context, conn *zitadel.Connection, orgID string, m *fixtures.MachineUser) (string, error) {
	c := user_pb.NewUserServiceClient(conn)
	return ensureMachineUser(ctx, c, orgID, m.Username)
}

// AddPAT mints a fresh personal access token for the given user and
// returns both the token ID (needed to later remove it) and the bearer
// token string (the secret). PATs cannot be listed after creation, so
// callers must persist the returned values.
func AddPAT(ctx context.Context, conn *zitadel.Connection, userID string) (string, string, error) {
	c := user_pb.NewUserServiceClient(conn)
	resp, err := c.AddPersonalAccessToken(ctx, &user_pb.AddPersonalAccessTokenRequest{
		UserId:         userID,
		ExpirationDate: timestamppb.New(time.Now().Add(patExpiry)),
	})
	if err != nil {
		return "", "", fmt.Errorf("add PAT: %w", err)
	}
	return resp.GetTokenId(), resp.GetToken(), nil
}

// RemovePAT deletes a single PAT from the given user. Other PATs on
// the same user are unaffected.
func RemovePAT(ctx context.Context, conn *zitadel.Connection, userID, tokenID string) error {
	c := user_pb.NewUserServiceClient(conn)
	_, err := c.RemovePersonalAccessToken(ctx, &user_pb.RemovePersonalAccessTokenRequest{
		UserId:  userID,
		TokenId: tokenID,
	})
	if err != nil {
		return fmt.Errorf("remove PAT: %w", err)
	}
	return nil
}

// AddMachineKey mints a fresh JWT-profile keypair on the given machine
// user and returns the key ID plus the JSON keyfile bytes (private key +
// clientId + keyId + audience metadata). The bytes are only available at
// creation time — Zitadel does not let callers list or re-export keys
// later — so the returned slice must be persisted (or fed straight into a
// token source) before the call returns.
//
// Uses the v2 user service's AddKey: when no public key is supplied,
// Zitadel generates the keypair server-side and returns the private JSON
// keyfile in KeyContent. The v2 surface is user-scoped (keyed by userID),
// so it needs neither the deprecated v1 management API nor x-zitadel-orgid
// org forwarding.
func AddMachineKey(ctx context.Context, conn *zitadel.Connection, userID string) (string, []byte, error) {
	c := user_pb.NewUserServiceClient(conn)
	resp, err := c.AddKey(ctx, &user_pb.AddKeyRequest{
		UserId:         userID,
		ExpirationDate: timestamppb.New(time.Now().Add(patExpiry)),
	})
	if err != nil {
		return "", nil, fmt.Errorf("add machine key: %w", err)
	}
	return resp.GetKeyId(), resp.GetKeyContent(), nil
}

// DeleteMachineUser removes the user from Zitadel. All of the user's
// PATs are invalidated as a side-effect.
func DeleteMachineUser(ctx context.Context, conn *zitadel.Connection, userID string) error {
	c := user_pb.NewUserServiceClient(conn)
	_, err := c.DeleteUser(ctx, &user_pb.DeleteUserRequest{UserId: userID})
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

func ensureMachineUser(ctx context.Context, c user_pb.UserServiceClient, orgID, username string) (string, error) {
	list, err := c.ListUsers(ctx, &user_pb.ListUsersRequest{
		Queries: []*user_pb.SearchQuery{{
			Query: &user_pb.SearchQuery_UserNameQuery{
				UserNameQuery: &user_pb.UserNameQuery{UserName: username},
			},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("list users %q: %w", username, err)
	}
	if len(list.GetResult()) > 0 {
		return list.GetResult()[0].GetUserId(), nil
	}

	resp, err := c.CreateUser(ctx, &user_pb.CreateUserRequest{
		OrganizationId: orgID,
		Username:       &username,
		UserType: &user_pb.CreateUserRequest_Machine_{
			Machine: &user_pb.CreateUserRequest_Machine{Name: username},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create machine user %q: %w", username, err)
	}
	return resp.GetId(), nil
}
