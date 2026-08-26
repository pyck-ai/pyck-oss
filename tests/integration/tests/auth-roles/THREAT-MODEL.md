# auth-roles: how a user could try to break the gate, and why they can't

This suite tests the per-service capability gate (#1293). The gate sits in front
of five services (inventory, picking, receiving, file, main-data) and requires
an authenticated, non-system user to hold `<service>_service` in **every**
tenant the request operates on. Management and workflow are intentionally
ungated.

Below is the attacker's-eye view: each row is a way someone might try to reach a
gated service without the right grant, the mechanism that stops them, and the
test that proves it. "Operative tenants" = the tenant set the tenant middleware
derives from `X-Pyck-Tenant-Id` (a single id, a comma list, or `all` → every
tenant the user has a ladder role in; absent → defaults to `all`).

| # | Attack / mistake | What stops it | Covering test |
|---|------------------|---------------|----------------|
| 1 | Call a gated service with a ladder role (writer/admin) but no service role | Gate denies 403 — the ladder is orthogonal to the gate | `TestGateDeniesWithoutServiceRole` (all 5 services) |
| 2 | Hold service X's role, use it to reach service Y | Gate checks the *specific* `<service>_service`, not "any service role" | `TestGateDeniesWrongServiceRole` (all 5) |
| 0 | Hit a gated service with no roles at all (no ladder, no service role) | Authenticated but empty: explicit tenant → 400 at the tenant layer; no tenant → empty operative set → 403 at the gate. Denied either way | `TestNoRolesDenied` |
| 3 | Grant yourself only the service role (skip the ladder), assume it's enough | A service role creates **no** ladder entry. With an explicit tenant the tenant middleware 400s (no reader); with no tenant the operative set is empty and the gate fails closed 403 | `TestServiceRoleAloneIsInsufficient` |
| 4 | Hold the role in tenant A, point the request at tenant B (where you're a member but lack it) | Service roles are per-tenant; gate denies 403 for B | `TestServiceRoleIsTenantScoped` (B case) |
| 5 | Use `X-Pyck-Tenant-Id: all` or `A,B` to average over tenants and slip past | Gate requires the role in *every* operative tenant; a single gap denies 403 | `TestServiceRoleIsTenantScoped` (all / A,B cases) |
| 6 | Send a request with no tenant header to dodge tenant scoping | Empty header defaults to `all` → the user's ladder tenants; same per-tenant check applies (or empty → 403) | `TestServiceRoleIsTenantScoped`, `TestServiceRoleAloneIsInsufficient` |
| 7 | Assign yourself a service role via the management API as a non-admin (or unauthenticated) | `assignRoles`/`removeRoles`/`serviceRoles`/`userServiceRoles` reject unauthenticated callers first, then require tenant-admin | `Test*RequiresAdmin` (4), `Test*RequiresAuth` (4) |
| 8 | Assign a privilege-ladder key (`admin`) or junk through `assignRoles`/`removeRoles` to escalate | Only `*_service` keys are accepted; anything else is rejected, and a mixed valid+invalid request is rejected whole (no partial apply, verified for both endpoints) | `Test*RejectsLadderKey`, `…RejectsUnknownKey`, `…RejectsMixedValidAndLadder` (assign + remove) |
| 9 | Assign or remove a role against a user in another tenant, a non-member, an unknown tenant, or a soft-deleted tenant, by guessing ids | Resolver validates tenant existence (incl. not-soft-deleted) and tenant membership before touching Zitadel, for both mutations and the read endpoint | `Test{Assign,Remove}RolesUnknownTenant`, `…UserNotInTenant`, `TestUserServiceRolesUnknownTenant`, `…UserNotInTenant`, `TestAssignRolesRejectsSoftDeletedTenant` |
| 10 | Removing one role quietly drops the user's other roles (or their ladder) | Removal is surgical: other service roles and the ladder are preserved; the Zitadel authorization is deleted only when nothing remains | `TestRemoveRolesPreservesOtherServiceRoles`, `…LastRoleLeavesEmpty` |
| 11 | Expect the gate to block anonymous traffic for you | It doesn't — the gate is transparent to unauthenticated requests by design (downstream auth decides); this is documented, not a hole | `TestUnauthenticatedFallsThrough` |
| 12 | Hold a real authorization (ladder + service role) in tenant B without being a home-org member there, then try to assign/remove roles by targeting tenant B | `resolveTenantUser` requires a `management.users` row scoped to the tenant; the zitadel-sync workflow only ever projects a user into their home org, so a cross-tenant *authorization* alone never creates one in B — the mutation can't resolve the target there at all | `TestAssignRolesCannotTargetNonHomeTenantMember`, `TestRemoveRolesCannotTargetNonHomeTenantMember` |

Positive controls (prove the checks aren't simply "deny all"): `TestGateAllowsWithServiceRole`, `TestSystemTokenBypassesAllGates`, `TestSystemTokenBypassesGateWithEmptyTenantSet`, `TestManagementNotGated`, `TestServiceRoleCoveredInBothTenantsAllowsMultiHeader` (multi-tenant header allows when fully covered, not just denies on a gap).

## Residual risks / properties worth knowing

- **Revocation is eventually-consistent.** After `removeRoles`, a token introspected *before* the change keeps its cached roles until the introspection cache TTL expires (or the PAT is revoked). The gate sees the cached snapshot. Mitigation: keep the introspection cache TTL short; `TestRemoveRoleRevokesGateAccess` polls with a **freshly minted** token each attempt, which forces a fresh introspection and reflects the revocation. Operators revoking access urgently should also revoke the PAT, not only the role.
- **System tokens bypass the gate entirely.** Anything holding `ROLE_SYSTEM` on its home org is upgraded to the system identity and skips every gate. This is intended (service-to-service traffic), but it makes system-credential hygiene the real control. Covered as a positive: `TestSystemTokenBypassesAllGates`.
- **Access is opt-in with no backfill.** Existing users are denied on gated services until an admin assigns roles; new tenants get the role *menu* (project grant) but individual users still need an explicit assignment. Not a hole — a deployment consideration.
- **Coverage guard for new services.** If a sixth `*_service` role is added to the `serviceroles` enum without a direct URL in the test config, `TestServiceCatalogHasURLs` fails and `serviceURL` panics loudly — so a new gated service can't silently escape the gate matrix in `ar_gate_test.go`.
- **`ROLE_ADMIN` has no in-product path to a real user.** `registerTenant`'s project grant only ever offers `reader`/`writer` + the service roles (see `addProjectGrantsInput` in `register-tenant/workflow.go`); `admin` is never included, and `assignRoles` explicitly refuses to grant it (`TestAssignRolesRejectsLadderKey`). So today every `Test*RequiresAdmin` case only proves a `writer` is denied — none of them, and no other test in this suite, exercises a genuine non-system caller holding `ROLE_ADMIN` succeeding. All positive "admin passes" coverage is implicitly via the system token (`adminClient()`), which bypasses the check entirely (`IsSystemUser()`). If a real per-tenant admin path is added later (e.g. an ops-only Zitadel-side grant, or a future `promoteToAdmin` mutation), add a positive test that mints one and calls `assignRoles`/`removeRoles`/`serviceRoles`/`userServiceRoles` with it — this is currently untestable without bypassing the product's own APIs to seed the grant.
- **The write endpoints (assignRoles/removeRoles/userServiceRoles) are single-tenant by construction, not just by an isolation check.** `resolveTenantUser` requires a `management.users` row scoped to `input.TenantID`, and that row is only ever created by the zitadel-sync workflow for a user's *home* org (`FetchZitadelUsersActivity` lists org membership via `GetAllOrganizationUsers`, not project authorizations). A user can hold real ladder/service-role authorizations in other tenants (exactly what `TestServiceRoleIsTenantScoped` exploits to test the gate), but those tenants can never resolve them as a mutation target — there is no `userAuthorization`-style cross-tenant leak to guard against on the write side, because the identity lookup fails first. Contrast with `userAuthorization(orgID, zitadelUserID)` itself, which *is* explicitly org-scoped (necessary for a user's home-tenant reconciliation to never touch another org's authorization by accident) — this is the same scoping bug class the test helper `EnsureProjectGrant` had until it was fixed (its idempotency check matched on user+project only, silently skipping a second tenant's grant). Covered by `TestAssignRolesCannotTargetNonHomeTenantMember` / `TestRemoveRolesCannotTargetNonHomeTenantMember` (`ar_cross_tenant_test.go`).

## Running

These tests need the live stack (`task up`) **with the #1293 gate build deployed**
and the integration env (`config/keys/bootstrap.env`). The gate cases hit each
service's direct port (config `*URL`), not the gateway, so a subgraph 403 is
observable instead of being folded into a GraphQL error.

```
task test:integration -- -run TestAuthRoles
```
