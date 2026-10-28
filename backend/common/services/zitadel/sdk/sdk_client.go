// Package sdk wraps the Zitadel client for the org and user listings the
// zitadel-sync workflow projects users from. Provisioning does not go
// through here: the register-tenant activities call the Zitadel v2 stubs
// directly, so this surface shrinks toward deletion together with the
// one-way sync it serves.
package sdk

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"
	managementclient "github.com/zitadel/zitadel-go/v3/pkg/client/management"
	"github.com/zitadel/zitadel-go/v3/pkg/client/zitadel"
	pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/management"
	object_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/object"
	orgv2_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/org/v2"
	user_pb "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/user"

	"github.com/pyck-ai/pyck/backend/common/log"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/dial"
)

const (
	defaultLimit = 1000

	orgOwner = "ORG_OWNER"
)

// ErrOrganizationNotFound is returned by GetOrganizationByID when no
// organization exists under the ID or Zitadel still projects it as REMOVED.
// Org-scoped listings (users, members) of a removed org answer with empty
// results rather than NotFound, so a caller that must tell "no members"
// from "no org" probes with GetOrganizationByID first.
var ErrOrganizationNotFound = errors.New("organization not found")

// ZitadelSdkClient serves the org-scoped listings of one Zitadel
// organization over a management API connection.
type ZitadelSdkClient struct {
	managementAPI *managementclient.Client
}

// SdkClient builds a management client with JWT profile credentials.
// grpcAddr is the host:port for gRPC dial (e.g. "localhost:8080").
// apiURL is the full HTTP URL for token exchange (e.g. "http://localhost:8080").
// When insecure is true, the connection uses plaintext gRPC and a custom token
// source that bypasses OIDC discovery.
func SdkClient(ctx context.Context, issuer, grpcAddr, apiURL, jwtProfilePath, orgID string, insecure bool) (*ZitadelSdkClient, error) {
	options := dial.ConnectionOptions(ctx, apiURL, issuer, jwtProfilePath, insecure)
	if orgID != "" {
		options = append(options, zitadel.WithOrgID(orgID))
	}

	managementClient, err := managementclient.NewClient(
		ctx, issuer, grpcAddr,
		[]string{oidc.ScopeOpenID, "urn:zitadel:iam:org:project:id:zitadel:aud"},
		options...,
	)
	if err != nil {
		return nil, err
	}

	return &ZitadelSdkClient{managementAPI: managementClient}, nil
}

// Close releases the management connection.
func (client *ZitadelSdkClient) Close() {
	if err := client.managementAPI.Connection.Close(); err != nil {
		logger := log.DefaultLogger().With().
			Str("component", "zitadel-client").
			Logger()
		logger.Warn().Err(err).Msg("could not close grpc management connection")
	}
}

// GetOrganizationByID looks the organization up through the org v2 API
// regardless of the client's org scope. An absent org and one projected as
// REMOVED are both reported as ErrOrganizationNotFound; any transport or
// server failure is wrapped and left for the caller's retry classification.
func (client *ZitadelSdkClient) GetOrganizationByID(ctx context.Context, orgID string) (*Org, error) {
	orgClient := orgv2_pb.NewOrganizationServiceClient(client.managementAPI.Connection)
	resp, err := orgClient.ListOrganizations(ctx, &orgv2_pb.ListOrganizationsRequest{
		Queries: []*orgv2_pb.SearchQuery{{
			Query: &orgv2_pb.SearchQuery_IdQuery{IdQuery: &orgv2_pb.OrganizationIDQuery{Id: orgID}},
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("lookup organization %q: %w", orgID, err)
	}
	for _, org := range resp.GetResult() {
		if org.GetId() == orgID && org.GetState() != orgv2_pb.OrganizationState_ORGANIZATION_STATE_REMOVED {
			return &Org{ID: org.GetId()}, nil
		}
	}

	return nil, fmt.Errorf("%w: %q", ErrOrganizationNotFound, orgID)
}

// GetOrganizationUsers lists one page of the client's organization's users.
func (client *ZitadelSdkClient) GetOrganizationUsers(ctx context.Context, skip uint64, limit uint32) ([]*UserProfile, error) {
	paginationQuery := &object_pb.ListQuery{Limit: limit, Offset: skip}
	resp, err := client.managementAPI.ListUsers(ctx, &pb.ListUsersRequest{Query: paginationQuery})
	if err != nil {
		return nil, err
	}

	users := []*UserProfile{}
	for _, user := range resp.Result {
		firstName := ""
		lastName := ""
		email := ""

		switch user.GetType().(type) {
		case *user_pb.User_Machine:
			machineUser := user.GetType().(*user_pb.User_Machine).Machine
			splitName := strings.SplitN(machineUser.Name, " ", 2)
			firstName = splitName[0]
			if len(splitName) > 1 {
				lastName = splitName[1]
			}
		case *user_pb.User_Human:
			humanUser := user.GetType().(*user_pb.User_Human).Human
			firstName = humanUser.Profile.FirstName
			lastName = humanUser.Profile.LastName
			email = humanUser.Email.Email
		}
		users = append(users, &UserProfile{
			ID:        user.Id,
			Username:  user.UserName,
			Email:     email,
			FirstName: firstName,
			LastName:  lastName,
		})
	}

	return users, nil
}

// GetAllOrganizationUsers pages through every user of the client's
// organization.
func (client *ZitadelSdkClient) GetAllOrganizationUsers(ctx context.Context) ([]*UserProfile, error) {
	users := []*UserProfile{}
	var skip uint64
	var limit uint32 = defaultLimit
	for {
		currentUsers, err := client.GetOrganizationUsers(ctx, skip, limit)
		if err != nil {
			return nil, err
		}
		users = append(users, currentUsers...)
		if len(currentUsers) < int(limit) {
			break
		}
		skip += uint64(limit)
	}

	return users, nil
}

// GetOrganizationOwners lists one page of the client's organization's
// members and returns the user IDs holding the owner role.
func (client *ZitadelSdkClient) GetOrganizationOwners(ctx context.Context, skip uint64, limit uint32) ([]string, error) {
	paginationQuery := &object_pb.ListQuery{Limit: limit, Offset: skip}
	response, err := client.managementAPI.ListOrgMembers(ctx, &pb.ListOrgMembersRequest{Query: paginationQuery})
	if err != nil {
		return nil, err
	}

	owners := []string{}
	for _, member := range response.Result {
		if slices.Contains(member.Roles, orgOwner) {
			owners = append(owners, member.UserId)
		}
	}

	return owners, nil
}

// GetAllOrganizationOwners pages through every owner of the client's
// organization.
func (client *ZitadelSdkClient) GetAllOrganizationOwners(ctx context.Context) ([]string, error) {
	owners := []string{}
	var skip uint64
	var limit uint32 = defaultLimit
	for {
		currentOwners, err := client.GetOrganizationOwners(ctx, skip, limit)
		if err != nil {
			return nil, err
		}
		owners = append(owners, currentOwners...)
		if len(currentOwners) < int(limit) {
			break
		}
		skip += uint64(limit)
	}

	return owners, nil
}
