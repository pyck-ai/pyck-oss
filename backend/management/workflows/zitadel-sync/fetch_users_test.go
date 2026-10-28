//nolint:testpackage // newOrgClient is the unexported test seam of Activities; tests live alongside.
package zitadel_sync

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
)

// fakeOrgClient answers the org probe with probeErr (nil = the org exists)
// and records whether any listing ran afterwards; a deleted org must never
// reach the listings because Zitadel answers them with empty results.
type fakeOrgClient struct {
	probeErr    error
	users       []*sdk.UserProfile
	owners      []string
	listedUsers bool
	closed      bool
}

func (f *fakeOrgClient) GetOrganizationByID(_ context.Context, orgID string) (*sdk.Org, error) {
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	return &sdk.Org{ID: orgID}, nil
}

func (f *fakeOrgClient) GetAllOrganizationUsers(context.Context) ([]*sdk.UserProfile, error) {
	f.listedUsers = true
	return f.users, nil
}

func (f *fakeOrgClient) GetAllOrganizationOwners(context.Context) ([]string, error) {
	return f.owners, nil
}

func (f *fakeOrgClient) Close() { f.closed = true }

func runFetchZitadelUsers(t *testing.T, fake *fakeOrgClient) ([]User, error) {
	t.Helper()
	a := &Activities{
		newOrgClient: func(context.Context, string) (orgUsersClient, error) { return fake, nil },
	}
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.FetchZitadelUsersActivity)

	val, err := env.ExecuteActivity(a.FetchZitadelUsersActivity, FetchZitadelUsersActivityInput{TenantID: "org-a"})
	if err != nil {
		return nil, err
	}
	var got []User
	require.NoError(t, val.Get(&got))
	return got, nil
}

// An org removed out of band fails the probe with the SDK sentinel; the
// activity must end there with the non-retryable ErrZitadelOrgNotFound so
// TenantSyncWorkflow skips the tenant, and must not list users: an empty
// listing would flow into ReconcileUsersActivity and soft-delete the
// tenant's whole user projection.
func TestFetchZitadelUsersActivity_SkipsRemovedOrgBeforeListing(t *testing.T) {
	t.Parallel()

	fake := &fakeOrgClient{
		probeErr: fmt.Errorf("%w: %q", sdk.ErrOrganizationNotFound, "org-a"),
		users:    []*sdk.UserProfile{{ID: "u1", Username: "alice"}},
	}

	got, err := runFetchZitadelUsers(t, fake)

	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, ErrZitadelOrgNotFound, appErr.Type())
	assert.True(t, appErr.NonRetryable(), "a missing org never comes back; retrying burns the budget")
	assert.Nil(t, got)
	assert.False(t, fake.listedUsers, "users of a removed org must never be listed")
	assert.True(t, fake.closed, "the org-scoped client is released on the skip path")
}

// A transport failure on the probe is not "org gone": it is returned
// unchanged (retryable) and the listings are still not attempted.
func TestFetchZitadelUsersActivity_ProbeTransportFailureStaysRetryable(t *testing.T) {
	t.Parallel()

	probeErr := errors.New("lookup organization \"org-a\": rpc error: code = Unavailable desc = zitadel restarting")
	fake := &fakeOrgClient{probeErr: probeErr}

	_, err := runFetchZitadelUsers(t, fake)

	// The activity environment wraps a plain error the way a worker does:
	// an untyped, retryable ApplicationError.
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	assert.NotEqual(t, ErrZitadelOrgNotFound, appErr.Type(), "transport failures must not be classified as a deleted org")
	assert.False(t, appErr.NonRetryable(), "the retry policy must get another attempt")
	require.ErrorContains(t, err, "zitadel restarting")
	assert.False(t, fake.listedUsers)
}

// Positive control: a live org is listed and its owners are flagged on the
// returned users, keyed to the tenant's org ID.
func TestFetchZitadelUsersActivity_ListsUsersOfLiveOrg(t *testing.T) {
	t.Parallel()

	fake := &fakeOrgClient{
		users: []*sdk.UserProfile{
			{ID: "u1", Username: "alice", Email: "alice@example.com", FirstName: "Alice", LastName: "A"},
			{ID: "u2", Username: "bob", Email: "bob@example.com"},
		},
		owners: []string{"u1"},
	}

	got, err := runFetchZitadelUsers(t, fake)
	require.NoError(t, err)

	assert.True(t, fake.listedUsers)
	assert.True(t, fake.closed)
	assert.Equal(t, []User{
		{ID: "u1", Username: "alice", Email: "alice@example.com", FirstName: "Alice", LastName: "A", TenantID: "org-a", IsOwner: true},
		{ID: "u2", Username: "bob", Email: "bob@example.com", TenantID: "org-a", IsOwner: false},
	}, got)
}
