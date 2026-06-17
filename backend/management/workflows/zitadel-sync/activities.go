package zitadel_sync

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"golang.org/x/sync/errgroup"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"
	common_tenant "github.com/pyck-ai/pyck/backend/common/tenant"
	"github.com/pyck-ai/pyck/backend/common/txid"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/tenant"
	"github.com/pyck-ai/pyck/backend/management/ent/gen/user"
)

// NewActivities wires dependencies for all Zitadel sync activities.
func NewActivities(ent *ent.Client, temporal client.Client, apiURL, grpcAddr, audience, keyFilePath, projectID string, tlsInsecure bool) *Activities {
	return &Activities{
		Ent:              ent,
		Temporal:         temporal,
		APIURL:           apiURL,
		GrpcAddr:         grpcAddr,
		Audience:         audience,
		KeyFilePath:      keyFilePath,
		ZitadelProjectID: projectID,
		TlsInsecure:      tlsInsecure,
	}
}

// Activities holds shared dependencies for all activities.
type Activities struct {
	Ent              *ent.Client
	Temporal         client.Client
	APIURL           string
	GrpcAddr         string
	Audience         string
	KeyFilePath      string
	ZitadelProjectID string
	TlsInsecure      bool
}

// FetchZitadelTenantsActivity returns all organizations from Zitadel.
func (a *Activities) FetchZitadelTenantsActivity(ctx context.Context, _ FetchZitadelTenantsActivityInput) ([]Tenant, error) {
	logger := activity.GetLogger(ctx)

	c, err := GetZitadelClient(ctx, a.APIURL, a.GrpcAddr, a.Audience, a.KeyFilePath, a.TlsInsecure, "")
	if err != nil {
		logger.Error("failed to get Zitadel client", "err", err)
		return nil, err
	}
	defer c.Close()

	orgs, err := c.GetAllOrganizations(ctx)
	if err != nil {
		logger.Error("failed to fetch Zitadel organizations", "err", err)
		return nil, err
	}

	out := make([]Tenant, len(orgs))
	for i, o := range orgs {
		out[i] = Tenant{ID: o.ID, Name: o.Name}
	}

	return out, nil
}

// FetchDbTenantsActivity returns all active tenants from the local DB.
func (a *Activities) FetchDbTenantsActivity(ctx context.Context, _ FetchDbTenantsInput) ([]Tenant, error) {
	logger := activity.GetLogger(ctx)
	serviceUserCtx := authn.Context(ctx, authn.SystemUser())

	dbTenants, err := a.Ent.Tenant.Query().
		Where(tenant.DeletedAtIsNil()).
		AllPages(serviceUserCtx, 100)
	if err != nil {
		logger.Error("failed to fetch tenants from DB", "err", err)
		return nil, err
	}

	out := make([]Tenant, len(dbTenants))
	for i, t := range dbTenants {
		out[i] = Tenant{
			ID:   t.IdpOrgRef,
			Name: t.Name,
		}
	}

	return out, nil
}

// ReconcileTenantsActivity creates, updates, or soft-deletes DB tenants to match Zitadel.
func (a *Activities) ReconcileTenantsActivity(ctx context.Context, input ReconcileTenantsActivityInput) (err error) {
	logger := activity.GetLogger(ctx)
	serviceUserCtx := authn.Context(ctx, authn.SystemUser())

	zitadelTenantsMap := make(map[string]Tenant, len(input.ZitadelTenants))
	for _, t := range input.ZitadelTenants {
		zitadelTenantsMap[t.ID] = t
	}

	dbTenantsMap := make(map[string]Tenant, len(input.DbTenants))
	for _, t := range input.DbTenants {
		dbTenantsMap[t.ID] = t
	}

	// Collect org IDs that exist in both Zitadel and DB (need metadata fetch)
	var matchedOrgIDs []string
	for id := range zitadelTenantsMap {
		if _, ok := dbTenantsMap[id]; ok {
			matchedOrgIDs = append(matchedOrgIDs, id)
		}
	}

	// Fetch metadata for all matched orgs in parallel using a single gRPC connection
	allMetadata := a.fetchAllOrgMetadata(ctx, matchedOrgIDs)

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

	for id, dbT := range dbTenantsMap {
		if _, ok := zitadelTenantsMap[id]; !ok {
			if e := tx.Tenant.Update().
				SetDeletedAt(time.Now().UTC()).
				Where(tenant.IDEQ(authn.ComputeUUID(a.Audience, dbT.ID))).
				Exec(serviceUserCtx); e != nil {
				logger.Error("failed to soft delete tenant", "tenant_id", dbT.ID, "name", dbT.Name, "err", e)
				err = e
				return err
			}
			logger.Debug("delete tenant", "tenant_id", dbT.ID)
		}
	}

	for id, zt := range zitadelTenantsMap {
		dbT, ok := dbTenantsMap[id]
		tenantID := authn.ComputeUUID(a.Audience, zt.ID)

		if !ok {
			logger.Debug("organization in Zitadel but not in database: use registerTenant mutation if sync needed",
				"zitadel_org_id", zt.ID,
				"name", zt.Name,
				"tenant_id", tenantID)
			continue
		}

		// Use pre-fetched metadata, converting boolean fields at ingestion
		var zitadelData map[string]any
		if metadata, ok := allMetadata[zt.ID]; ok && len(metadata) > 0 {
			zitadelData = make(map[string]any, len(metadata))
			for k, v := range metadata {
				if core.IsBooleanField(k) {
					if b, err := strconv.ParseBool(v); err == nil {
						zitadelData[k] = b
						continue
					}
				}
				zitadelData[k] = v
			}
		}

		// Read existing tenant to merge data (preserves DB-only keys like computed URLs)
		existingTenant, getErr := tx.Tenant.Get(serviceUserCtx, tenantID)
		if getErr != nil {
			logger.Error("failed to fetch tenant for data merge", "tenant_id", dbT.ID, "err", getErr)
			err = getErr
			return err
		}

		// Merge + enrich via extracted pipeline function
		mergedData := core.ReconcileTenantData(existingTenant.Data, zitadelData,
			tenantID.String(), core.Config.FrontendBaseURL, core.Config.EnvironmentName)

		// Check if update is needed
		needsNameUpdate := zt.Name != dbT.Name
		needsDataUpdate := !core.MapsEqual(existingTenant.Data, mergedData)

		if needsNameUpdate || needsDataUpdate {
			updateOp := tx.Tenant.Update().Where(tenant.IDEQ(tenantID))

			if needsNameUpdate {
				updateOp = updateOp.SetName(zt.Name)
			}

			if needsDataUpdate {
				updateOp = updateOp.SetData(mergedData)
			}

			if _, e := updateOp.Save(serviceUserCtx); e != nil {
				logger.Error("failed to update tenant", "tenant_id", dbT.ID, "err", e)
				err = e
				return err
			}
			logger.Debug("updated tenant", "tenant_id", dbT.ID, "name_updated", needsNameUpdate, "data_updated", needsDataUpdate)
		}
	}

	if err = tx.Commit(); err != nil {
		logger.Error("failed to commit transaction", "err", err)
		return err
	}

	return nil
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

// FetchZitadelUsersActivity returns all users/owners/roles from Zitadel for a tenant.
func (a *Activities) FetchZitadelUsersActivity(ctx context.Context, input FetchZitadelUsersActivityInput) ([]User, error) {
	logger := activity.GetLogger(ctx)

	c, err := GetZitadelClient(ctx, a.APIURL, a.GrpcAddr, a.Audience, a.KeyFilePath, a.TlsInsecure, input.TenantID)
	if err != nil {
		logger.Error("failed to get Zitadel client", "err", err)
		return nil, err
	}
	defer c.Close()

	users, err := c.GetAllOrganizationUsers(ctx)
	if err != nil {
		logger.Error("failed to fetch Zitadel users", "err", err)
		return nil, err
	}

	owners, err := c.GetAllOrganizationOwners(ctx)
	if err != nil {
		logger.Error("failed to fetch Zitadel owners", "err", err)
		return nil, err
	}

	roles, err := c.GetAllOrganizationUsersRoles(ctx, a.ZitadelProjectID)
	if err != nil {
		logger.Error("failed to fetch Zitadel user roles", "err", err)
		return nil, err
	}

	ownersMap := make(map[string]bool, len(owners))
	for _, id := range owners {
		ownersMap[id] = true
	}

	userRolesMap := make(map[string][]string, len(roles))
	for _, ur := range roles {
		userRolesMap[ur.ID] = ur.Roles
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
			Roles:     userRolesMap[u.ID],
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
		//TODO Remove when fully switching to rbac roles
		/*var roles []string
		  if relRoles, e := u.Roles(serviceUserCtx); e == nil {
		  	for _, rr := range relRoles {
		  		roles = append(roles, rr.Name)
		  	}
		  }*/

		out = append(out, User{
			ID:        u.IdpID,
			Username:  u.Username,
			Email:     u.Email,
			FirstName: u.FirstName,
			LastName:  u.LastName,
			TenantID:  input.TenantID,
			Roles:     canonRoles(u.LegacyRoles),
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

	for i := range input.ZitadelUsers {
		input.ZitadelUsers[i].Roles = canonRoles(input.ZitadelUsers[i].Roles)
	}

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
				SetLegacyRoles(zu.Roles).
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
			zu.IsOwner == dbu.IsOwner &&
			stringSlicesEqual(zu.Roles, dbu.Roles) {
			continue
		}

		uu := tx.User.Update().
			Where(user.And(user.IdpID(zu.ID), user.TenantID(tenantUUID)))

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

		if !stringSlicesEqual(zu.Roles, dbu.Roles) {
			uu = uu.SetLegacyRoles(zu.Roles)
		}

		if _, e := uu.Save(serviceUserCtx); e != nil {
			logger.Error("failed to update user", "idp_id", zu.ID, "username", zu.Username, "err", e)
			err = e
			return err
		}
		logger.Debug("updated user", "idp_id", zu.ID, "username", zu.Username)
	}

	for id, dbu := range dbUsersMap {
		if _, ok := zitadelUsersMap[id]; !ok {
			if e := tx.User.Update().
				SetDeletedAt(time.Now().UTC()).
				Where(user.And(user.IdpID(dbu.ID), user.TenantID(tenantUUID))).
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

// fetchAllOrgMetadata retrieves metadata for all given org IDs using a single
// shared gRPC connection and concurrent workers. This avoids the overhead of
// creating a new TLS connection per organization.
func (a *Activities) fetchAllOrgMetadata(ctx context.Context, orgIDs []string) map[string]map[string]string {
	logger := activity.GetLogger(ctx)

	if len(orgIDs) == 0 {
		return nil
	}

	c, err := GetZitadelClient(ctx, a.APIURL, a.GrpcAddr, a.Audience, a.KeyFilePath, a.TlsInsecure, "")
	if err != nil {
		logger.Error("failed to create shared Zitadel client for metadata fetch", "err", err)
		return nil
	}
	defer c.Close()

	const maxWorkers = 20
	results := make(map[string]map[string]string, len(orgIDs))
	var mu sync.Mutex

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(maxWorkers)

	for _, orgID := range orgIDs {
		g.Go(func() error {
			md, fetchErr := c.ListOrgMetadataForOrg(gCtx, orgID)
			if fetchErr != nil {
				logger.Warn("failed to fetch metadata for org", "org_id", orgID, "err", fetchErr)
				return nil // non-fatal: skip this org
			}

			mu.Lock()
			results[orgID] = md
			mu.Unlock()
			return nil
		})
	}

	_ = g.Wait() // errors already handled per-goroutine
	logger.Info("fetched org metadata", "total", len(orgIDs), "success", len(results))
	return results
}

// GetZitadelClient builds a Zitadel SDK client for the given API URL and audience.
func GetZitadelClient(ctx context.Context, apiURL, grpcAddr, audience, keyFilePath string, tlsInsecure bool, orgID string) (*sdk.ZitadelSdkClient, error) {
	return sdk.SdkClient(ctx, audience, grpcAddr, apiURL, keyFilePath, orgID, tlsInsecure)
}


// stringSlicesEqual compares two string slices as sets (order-insensitive).
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		if m[s] == 0 {
			return false
		}
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

// canonRoles returns a sorted, de-duplicated copy of roles.
func canonRoles(in []string) []string {
	if len(in) == 0 {
		return nil
	}

	m := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))

	for _, r := range in {
		if r == "" {
			continue
		}

		if _, ok := m[r]; !ok {
			m[r] = struct{}{}
			out = append(out, r)
		}
	}

	sort.Strings(out)

	return out
}
