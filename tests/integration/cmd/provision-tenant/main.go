//go:build integration

// Command provision-tenant registers a tenant through the gateway and mints
// a PAT inside it, then prints what a caller needs as shell assignments:
//
//	PYCK_TEST_TENANT_ID=<uuid>
//	PYCK_TEST_AUTH_TOKEN=<pat>
//	PYCK_TEST_ADMIN_USERNAME=<login name>
//	PYCK_TEST_ADMIN_PASSWORD=<password>
//
// It exists because a token is only served while a live tenant row backs it
// (see docs/tenant-access-control.md), so test tooling can no longer borrow
// a bootstrap-provisioned account from an organization that is not a tenant.
// The PAT is for headless callers; it is minted with admin plus the service
// gates, so it passes the admin-gated resolvers on its own tenant: the
// Bruno collection sends it on every request. The admin credentials are
// for the ones that log in interactively, the Bruno collection's OAuth
// step, say: the root organization is not a tenant, so a user homed in it
// authenticates nothing, and a login has to be a tenant's own admin.
// Registration grants that admin the same admin on the project grant a
// tenant org receives, which carries the full admin/writer/reader ladder
// plus the service gates; admin implies the rest on every consumer.
//
//	source tests/integration/scripts/envrc.sh
//	eval "$(cd tests/integration && go run -tags=integration ./cmd/provision-tenant)"
//
// With no -name every run registers a fresh tenant with generated
// credentials. Passing -name makes the tenant a durable fixture: a live
// tenant already carrying that name is reused rather than re-registered,
// which would fail on the name's unique index. Re-running against a reused
// tenant mints an additional PAT on the same machine user; they expire in a
// year and nothing reads the old ones, so they are left alone.
//
// Progress goes to stderr, so only the assignments reach stdout.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/services/zitadel/sdk"

	"github.com/pyck-ai/pyck/tests/integration/internal/config"
	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/internal/zitadelclient"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// errTenantNotServing reports a reusable name held by a row whose
// registration did not finish.
var errTenantNotServing = errors.New("tenant exists but its registration did not finish")

func main() {
	os.Exit(realMain())
}

func realMain() int {
	name := flag.String("name", "", "tenant name; reused when a live tenant already carries it (default: generated, always fresh)")
	adminUsername := flag.String("admin-username", "", "login name of the tenant's human admin (default: generated)")
	adminPassword := flag.String("admin-password", "", "password of the tenant's human admin (default: generated)")
	machineUser := flag.String("machine-user", "", "machine user to mint the PAT on (default: generated)")
	flag.Parse()

	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		return 1
	}

	conn, err := zitadelclient.Dial(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial zitadel: %v\n", err)
		return 1
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "close zitadel connection: %v\n", cerr)
		}
	}()

	tenant := fixtures.NewTenant()
	if *name != "" {
		tenant.Name = *name
	}
	if *adminUsername != "" {
		tenant.AdminUsername = *adminUsername
		tenant.AdminEmail = fmt.Sprintf("%s@%s.test", tenant.AdminUsername, tenant.Name)
	}
	if *adminPassword != "" {
		tenant.AdminPassword = *adminPassword
	}

	rt, reused, err := resolveTenant(ctx, cfg, tenant, *name != "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	machine := fixtures.NewMachineUser()
	if *machineUser != "" {
		machine.Username = *machineUser
	}

	// admin opens the admin-gated resolvers (userServiceRoles, assignRoles)
	// and implies writer and reader on every consumer of the ladder; the
	// service gates are orthogonal to the ladder while a Bruno run crosses
	// every gated subgraph, so the PAT needs admin plus the gates.
	roles := tests.RolesWithServiceGates(authn.ROLE_ADMIN.String())

	// A user may only hold roles the org's project grant lists, and a tenant
	// registered before the platform delegated a role carries the menu of
	// its own registration: granting the PAT admin there would fail with
	// Errors.Project.Role.NotFound. Widen the menu first; on a tenant
	// registered by a current build this is a read and no write.
	if _, _, err := sdk.ExtendProjectGrantRoles(ctx, sdk.NewGrantClient(conn), cfg.ZitadelProjectID, rt.IdpOrgRef, roles); err != nil {
		fmt.Fprintf(os.Stderr, "extend project grant menu of org %s: %v\n", rt.IdpOrgRef, err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "provisioning machine user %q + PAT in org %s...\n", machine.Username, rt.IdpOrgRef)
	pat, err := tests.ProvisionUserInTenantAs(ctx, cfg, conn, rt, machine, roles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provision user: %v\n", err)
		return 1
	}

	fmt.Printf("PYCK_TEST_TENANT_ID=%s\n", rt.ID)
	fmt.Printf("PYCK_TEST_AUTH_TOKEN=%s\n", pat.PAT)
	// A reused tenant's admin credentials are whatever created it, and
	// Zitadel will not hand a password back, so they are only echoed when
	// the caller supplied them. Printing the generated fixture there would
	// be a login that does not exist.
	if !reused || *adminUsername != "" {
		fmt.Printf("PYCK_TEST_ADMIN_USERNAME=%s\n", tenant.AdminUsername)
		fmt.Printf("PYCK_TEST_ADMIN_PASSWORD=%s\n", tenant.AdminPassword)
	}
	return 0
}

// resolveTenant returns the tenant to provision into (an existing live one
// carrying the requested name, or a freshly registered one) and whether it
// was reused. Reuse is only attempted for a caller-supplied name, because a
// generated name is meant to be unique per run.
//
// A reused tenant is required to be done: a pending or error row is a
// registration that has not finished, which the liveness axis refuses, so a
// PAT minted inside it would authenticate nothing.
func resolveTenant(ctx context.Context, cfg *config.Config, tenant *fixtures.Tenant, reuse bool) (*gateway.RegisteredTenant, bool, error) {
	if reuse {
		existing, err := gateway.FindTenantByName(ctx, cfg, tenant.Name)
		switch {
		case err == nil:
			if existing.Status != "done" {
				return nil, false, fmt.Errorf("%w: %q is %q; deleteTenant does not free its name, so wipe the stack (docker compose down -v && task up) and re-run", errTenantNotServing, tenant.Name, existing.Status)
			}
			fmt.Fprintf(os.Stderr, "reusing tenant %q (%s)\n", existing.Name, existing.ID)
			return existing, true, nil
		case !errors.Is(err, gateway.ErrTenantNotFound):
			return nil, false, fmt.Errorf("look up tenant %q: %w", tenant.Name, err)
		}
	}

	fmt.Fprintf(os.Stderr, "registering tenant %q...\n", tenant.Name)
	rt, err := gateway.RegisterTenant(ctx, cfg, tenant)
	if err != nil {
		return nil, false, fmt.Errorf("register tenant: %w", err)
	}
	return rt, false, nil
}
