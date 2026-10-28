package zitadel_sync

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
	common_tenant "github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/txid"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/user"
)

// ErrZitadelOrgNotFound is the application-error type returned by
// FetchZitadelUsersActivity when a tenant's Zitadel org no longer exists
// (deleted out of band). It is non-retryable so TenantSyncWorkflow skips
// the tenant instead of burning its retry budget. Tenant liveness is
// DB-owned: nothing soft-deletes the row because its org is gone, so the
// tenant stays listed (with its last projected users) every cycle until an
// operator disables it through deleteTenant, whose Zitadel deactivation
// tolerates NotFound.
const ErrZitadelOrgNotFound = "ZitadelOrgNotFound"

// orgNotFoundErr maps "the org is gone" (a Zitadel NotFound gRPC status or
// the SDK's sdk.ErrOrganizationNotFound from the org probe) to a
// non-retryable application error so per-tenant user sync can skip a
// deleted org rather than retry an operation that can never succeed. Any
// other error is returned unchanged (and stays retryable).
func orgNotFoundErr(err error) error {
	if status.Code(err) == codes.NotFound || errors.Is(err, sdk.ErrOrganizationNotFound) {
		return temporal.NewNonRetryableApplicationError(
			"zitadel org no longer exists", ErrZitadelOrgNotFound, err)
	}
	return err
}

// NewActivities wires dependencies for all Zitadel sync activities. The
// Zitadel client is opened per activity call, scoped to the tenant's org,
// from the service-account key at keyFilePath.
func NewActivities(ent *ent.Client, temporal client.Client, apiURL, grpcAddr, audience, keyFilePath string, tlsInsecure bool) *Activities {
	return &Activities{
		Ent:      ent,
		Temporal: temporal,
		Audience: audience,
		newOrgClient: func(ctx context.Context, orgID string) (orgUsersClient, error) {
			c, err := sdk.SdkClient(ctx, audience, grpcAddr, apiURL, keyFilePath, orgID, tlsInsecure)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
	}
}

// orgUsersClient is the part of the Zitadel SDK FetchZitadelUsersActivity
// uses, scoped to one org. *sdk.ZitadelSdkClient implements it; tests
// substitute a fake to observe the org probe without a gRPC connection.
type orgUsersClient interface {
	GetOrganizationByID(ctx context.Context, orgID string) (*sdk.Org, error)
	GetAllOrganizationUsers(ctx context.Context) ([]*sdk.UserProfile, error)
	GetAllOrganizationOwners(ctx context.Context) ([]string, error)
	Close()
}

// Activities holds shared dependencies for all activities.
type Activities struct {
	Ent      *ent.Client
	Temporal client.Client
	Audience string
	// newOrgClient opens a Zitadel client scoped to the given org ID; set by
	// NewActivities, required by FetchZitadelUsersActivity.
	newOrgClient func(ctx context.Context, orgID string) (orgUsersClient, error)
}

// FetchDbTenantsActivity returns the Zitadel org IDs of all live, fully
// registered tenants. Pending rows are excluded because their org does not
// exist yet; a system token skips the tenant privacy filter, so the
// predicate is the whole scope of this all-tenant listing. DeletedAtIsNil
// duplicates mixin.HistoryMixinQueryFilter, which the policy skips under
// FEATURE_SHOW_DELETED: the listing's scope must not depend on which
// features the activity context happens to carry.
func (a *Activities) FetchDbTenantsActivity(ctx context.Context, _ FetchDbTenantsInput) ([]string, error) {
	logger := activity.GetLogger(ctx)
	serviceUserCtx := authn.Context(ctx, authn.SystemUser())

	dbTenants, err := a.Ent.Tenant.Query().
		Where(tenant.DeletedAtIsNil(), tenant.StatusEQ(tenant.StatusDone)).
		AllPages(serviceUserCtx, 100)
	if err != nil {
		logger.Error("failed to fetch tenants from DB", "err", err)
		return nil, err
	}

	orgIDs := make([]string, len(dbTenants))
	for i, t := range dbTenants {
		orgIDs[i] = t.IdpOrgRef
	}

	return orgIDs, nil
}

// StartTenantSyncActivity starts user sync for specific tenant
func (a *Activities) StartTenantSyncActivity(ctx context.Context, in StartTenantSyncActivityInput) error {
	if in.TaskQueue == "" {
		in.TaskQueue = TenantSyncTaskQueue
	}

	wfidPrefix := in.WorkflowIDPrefix
	if wfidPrefix == "" {
		wfidPrefix = TenantWorkflowIDPrefix
	}
	wfID := wfidPrefix + in.TenantID

	opts := client.StartWorkflowOptions{
		ID:                    wfID,
		TaskQueue:             in.TaskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	}

	_, err := a.Temporal.ExecuteWorkflow(ctx, opts, TenantSyncWorkflow, TenantSyncWorkflowInput{
		TenantID: in.TenantID,
	})
	if err != nil {
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &already) {
			return nil
		}

		return err
	}

	return nil
}

// FetchZitadelUsersActivity returns all users/owners from Zitadel for a
// tenant, or ErrZitadelOrgNotFound when the tenant's org is gone.
func (a *Activities) FetchZitadelUsersActivity(ctx context.Context, input FetchZitadelUsersActivityInput) ([]User, error) {
	logger := activity.GetLogger(ctx)

	c, err := a.newOrgClient(ctx, input.TenantID)
	if err != nil {
		logger.Error("failed to get Zitadel client", "err", err)
		return nil, err
	}
	defer c.Close()

	// Zitadel answers the org-scoped user and member listings of a removed
	// org with empty results, not NotFound; reconciling that would
	// soft-delete the tenant's whole user projection. The org is therefore
	// probed first, and a missing one ends the sync here.
	if _, err := c.GetOrganizationByID(ctx, input.TenantID); err != nil {
		if !errors.Is(err, sdk.ErrOrganizationNotFound) {
			logger.Error("failed to look up Zitadel org", "err", err)
		}
		return nil, orgNotFoundErr(err)
	}

	users, err := c.GetAllOrganizationUsers(ctx)
	if err != nil {
		logger.Error("failed to fetch Zitadel users", "err", err)
		return nil, orgNotFoundErr(err)
	}

	owners, err := c.GetAllOrganizationOwners(ctx)
	if err != nil {
		logger.Error("failed to fetch Zitadel owners", "err", err)
		return nil, orgNotFoundErr(err)
	}

	ownersMap := make(map[string]bool, len(owners))
	for _, id := range owners {
		ownersMap[id] = true
	}

	out := make([]User, len(users))
	for i, u := range users {
		out[i] = User{
			ID:        u.ID,
			Username:  u.Username,
			Email:     u.Email,
			FirstName: u.FirstName,
			LastName:  u.LastName,
			TenantID:  input.TenantID,
			IsOwner:   ownersMap[u.ID],
		}
	}

	return out, nil
}

// FetchDbUsersActivity returns all active users for a tenant from the local DB.
func (a *Activities) FetchDbUsersActivity(ctx context.Context, input FetchDbUsersActivityInput) ([]User, error) {
	logger := activity.GetLogger(ctx)
	serviceUserCtx := authn.Context(ctx, authn.SystemUser())

	tenantObj, err := a.Ent.Tenant.Query().
		Where(tenant.IdpOrgRefEQ(input.TenantID)).
		Only(serviceUserCtx)
	if err != nil {
		logger.Error("failed to fetch tenant", "tenant_id", input.TenantID, "err", err)
		return nil, err
	}

	dbUsers, err := a.Ent.User.Query().
		Where(
			user.And(
				user.TenantID(tenantObj.ID),
				user.DeletedByIsNil(),
			),
		).
		AllPages(serviceUserCtx, 100)
	if err != nil {
		logger.Error("failed to fetch users from DB", "tenant_id", input.TenantID, "err", err)
		return nil, err
	}

	out := make([]User, 0, len(dbUsers))
	for _, u := range dbUsers {
		out = append(out, User{
			ID:        u.IdpID,
			Username:  u.Username,
			Email:     u.Email,
			FirstName: u.FirstName,
			LastName:  u.LastName,
			TenantID:  input.TenantID,
			IsOwner:   u.IsAdmin,
		})
	}

	return out, nil
}

// ReconcileUsersActivity upserts and soft-deletes DB users to match Zitadel state.
func (a *Activities) ReconcileUsersActivity(ctx context.Context, input ReconcileUsersActivityInput) (err error) {
	logger := activity.GetLogger(ctx)
	tenantUUID := authn.ComputeUUID(a.Audience, input.TenantID)
	serviceUserCtx := authn.Context(ctx, authn.SystemUser())
	serviceUserCtx = common_tenant.Context(serviceUserCtx, tenantUUID)

	zitadelUsersMap := make(map[string]User, len(input.ZitadelUsers))
	for _, u := range input.ZitadelUsers {
		zitadelUsersMap[u.ID] = u
	}

	dbUsersMap := make(map[string]User, len(input.DbUsers))
	for _, u := range input.DbUsers {
		dbUsersMap[u.ID] = u
	}

	serviceUserCtx = txid.With(serviceUserCtx, txid.New())
	tx, err := a.Ent.Tx(serviceUserCtx)
	if err != nil {
		logger.Error("failed to start transaction", "err", err)
		return err
	}
	serviceUserCtx = ent.NewTxContext(serviceUserCtx, tx)
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for id, zu := range zitadelUsersMap {
		dbu, exists := dbUsersMap[id]
		if !exists {
			if _, e := tx.User.Create().
				SetID(authn.ComputeUUID(a.Audience, zu.ID)).
				SetIdpID(zu.ID).
				SetUsername(zu.Username).
				SetEmail(zu.Email).
				SetFirstName(zu.FirstName).
				SetLastName(zu.LastName).
				SetTenantID(tenantUUID).
				SetIsAdmin(zu.IsOwner).
				Save(serviceUserCtx); e != nil {
				logger.Error("failed to create user", "idp_id", zu.ID, "username", zu.Username, "err", e)
				err = e
				return err
			}
			logger.Debug("created user", "idp_id", zu.ID, "username", zu.Username)
			continue
		}

		if zu.Username == dbu.Username &&
			zu.Email == dbu.Email &&
			zu.FirstName == dbu.FirstName &&
			zu.LastName == dbu.LastName &&
			zu.IsOwner == dbu.IsOwner {
			continue
		}

		// (idp_id, tenant_id) is unique among live rows, so this resolves at
		// most one user; the single-row write emits the user's own event.
		userID, found, e := liveUserID(serviceUserCtx, tx, zu.ID, tenantUUID)
		if e != nil {
			logger.Error("failed to look up user", "idp_id", zu.ID, "username", zu.Username, "err", e)
			err = e
			return err
		}
		if !found {
			// Soft-deleted or removed since the DB snapshot: nothing to update.
			logger.Debug("user to update no longer active; skipping", "idp_id", zu.ID)
			continue
		}
		uu := tx.User.UpdateOneID(userID)

		if zu.Username != dbu.Username {
			uu = uu.SetUsername(zu.Username)
		}

		if zu.Email != dbu.Email {
			uu = uu.SetEmail(zu.Email)
		}

		if zu.FirstName != dbu.FirstName {
			uu = uu.SetFirstName(zu.FirstName)
		}

		if zu.LastName != dbu.LastName {
			uu = uu.SetLastName(zu.LastName)
		}

		if zu.IsOwner != dbu.IsOwner {
			uu = uu.SetIsAdmin(zu.IsOwner)
		}

		if e := uu.Exec(serviceUserCtx); e != nil {
			logger.Error("failed to update user", "idp_id", zu.ID, "username", zu.Username, "err", e)
			err = e
			return err
		}
		logger.Debug("updated user", "idp_id", zu.ID, "username", zu.Username)
	}

	for id, dbu := range dbUsersMap {
		if _, ok := zitadelUsersMap[id]; !ok {
			userID, found, e := liveUserID(serviceUserCtx, tx, dbu.ID, tenantUUID)
			if e != nil {
				logger.Error("failed to look up user", "idp_id", dbu.ID, "username", dbu.Username, "err", e)
				err = e
				return err
			}
			if !found {
				// Already soft-deleted or removed since the DB snapshot.
				logger.Debug("user to soft-delete no longer active; skipping", "idp_id", dbu.ID)
				continue
			}
			if e := tx.User.UpdateOneID(userID).
				SetDeletedAt(time.Now().UTC()).
				Exec(serviceUserCtx); e != nil {
				logger.Error("failed to soft delete user", "idp_id", dbu.ID, "username", dbu.Username, "err", e)
				err = e
				return err
			}
			logger.Debug("soft deleted user", "idp_id", dbu.ID, "username", dbu.Username)
		}
	}

	if err = tx.Commit(); err != nil {
		logger.Error("failed to commit transaction", "err", err)
		return err
	}

	return nil
}

// liveUserID resolves the ID of the not-deleted user with the given Zitadel
// ID in the tenant. The User schema's unique index on (idp_id, tenant_id)
// covers live rows only, so there is at most one. found is false when none
// exists.
func liveUserID(ctx context.Context, tx *ent.Tx, idpID string, tenantID uuid.UUID) (id uuid.UUID, found bool, err error) {
	id, err = tx.User.Query().
		Where(user.IdpID(idpID), user.TenantID(tenantID), user.DeletedAtIsNil()).
		OnlyID(ctx)
	if ent.IsNotFound(err) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}
