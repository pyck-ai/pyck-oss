# Tenant access control

Every request to a pyck service carries two independent identity signals:

- the **bearer token** (Zitadel PAT / access token) — *who* is calling, and
  which tenants they hold role grants in;
- the **`X-Pyck-Tenant-Id` header** — *which tenants* the request acts on.

This document describes how the two are authorized, with the resulting access
matrix. The code lives in `backend/common/authn` (token path), `backend/common/liveness` (verdict cache) and
`backend/common/tenant` (header path).

## The header

`X-Pyck-Tenant-Id` accepts, in any combination (repeated headers and
comma-separated lists are equivalent):

| Value | Meaning |
|---|---|
| `<uuid>[,<uuid>…]` | *Explicit* tenants. Each one must be one the caller holds at least `READER` on. |
| `all` | *Expansion*: every tenant the caller holds a role in. |
| *(absent)* | Same as `all`. This is the default — most reads never send the header. |
| `all,<uuid>` | Both: the expansion **plus** the named tenant, which is validated as if requested explicitly. |

The parsed result keeps explicit and `all`-expanded tenants apart; the middleware turns
it into the request's *acting set* (`tenant.ForContext(ctx)`), which every
resolver uses to scope its queries (`tenant_id IN (…)`). Mutations require an
acting set of exactly one tenant (`request.MutationTenantID`).

## Two axes of authorization

`tenant.Middleware` authorizes the acting set on two axes, in this order:

1. **Role** — `user.HasRole(READER, id)` must hold for **every** tenant in
   the acting set, `all`-expanded ones included, else `400 no access to
   tenant ID` for the whole request — the pre-existing all-or-nothing rule;
   the expansion is never filtered down to the accessible subset.
2. **Liveness**: no request acts on a tenant that no live `tenant` row backs
   (soft-deleted by `deleteTenant`, the expiry sweep or reconcile; carrying a
   registration that did not finish; or absent from the table entirely). The
   middleware checks the whole acting set **plus the caller's home tenant**
   (a disabled tenant's users lose access everywhere at the row flip, not
   only once Zitadel deactivates their org) against the service's liveness
   cache under `pyck:tenants:<uuid>`, and only for callers that passed the
   role axis — a caller without a grant on a tenant learns nothing about
   that tenant's state.

Liveness applies two different rules depending on *how* a disabled tenant got
into the acting set:

- **Explicitly named → deny the whole request** (`403 tenant is disabled
  "<id>"`). The caller asked for that tenant by name; silently narrowing the
  scope would hide the denial and let a client believe it operated on it.
- **Reached only through `all` → filter it out** and proceed on the rest.
  `all` means "everything I may see", and a disabled tenant is by definition
  not that. Since `all` is also the no-header default, denying here would lock
  a multi-tenant user out of *every* tenant the moment any one of them is
  disabled — and break every client that omits the header.

Filtering settles the **mutation target** as well, because the acting set the
filter produces is the one `request.MutationTenantID` counts. A caller with
grants in two tenants sending no header has no unambiguous target and any
mutation is refused; disable one of the two and the survivor becomes the
implicit target, so a mutation that used to be refused now applies to the
remaining live tenant. That is the intended reading of both rules — a request
may only act on tenants in its acting set, and one of them is unambiguous —
but note that a tenant's state, not only the header, can decide whether a
mutation has a target.

The token owner's *own* tenant is additionally validated at authentication
time (`authn.checkTenantLive`): a user whose tenant is disabled gets `401`
before the header is even parsed. It is the same question the header side
asks, against the same source and under the same cache key, so a request that
passes here and then names its own tenant costs one lookup rather than two.
It matters on its own for the two callers that authenticate *without* the
tenant middleware (the NATS auth callout and the Temporal claim mapper)
where it is the only liveness gate on the path.

Both sides read management's tenant table and nothing else. Tenants are
registered DB-first and flow one way to Zitadel, so the row changes state
first and nothing writes tenant state back; asking Zitadel would answer later,
with a value derived from that same table. The consequence is that an
organization deactivated in the Zitadel console out of band does not revoke
anything: pyck does not treat Zitadel as a writer of tenant state.

### An ID no row backs is denied

The rule is "allow only what a live row backs", not "deny only what is
explicitly disabled". A token naming a tenant this deployment has no row for
authenticates nothing, and management logs the IDs it was asked about at
`Warn`. There are two ways to reach that log line, and both want an
operator's eye: a registration that never produced a row, and a token issued
for an organization that is not a tenant. Callers cache the verdict, so a
client retrying with the same token logs once per cache TTL per replica.

**System callers are the only exemption.** They return before the check, which
is what keeps `deleteTenant` and `restoreTenant` able to retire a row the
axis denies, and it is how every platform identity that legitimately has no
tenant reaches the API: the bootstrap service user, and pyck-admin, whose
human token is never forwarded to the gateway at all (the site calls through
its own `system` PAT).

**What this costs**: a human or machine user created directly in an
organization that is not a tenant (the instance root organization, say) can
no longer authenticate against pyck, even holding project roles. Such an
identity needs a tenant of its own. Test tooling that used to borrow a
bootstrap-provisioned account in a non-tenant organization must register its
own tenant instead.

The header-side check is what closes the gap for **cross-tenant grants**:
Zitadel keeps returning role grants into a deactivated org, so without it a
user homed in an active tenant kept reaching a disabled tenant it had a grant
into.

The check asks about the **tenant only**. Its input is the token's tenant ID
(the webhook-injected `pyck_tenant_id` claim, or `ComputeUUID(issuer,
resource_owner)`) and its verdict is shared by every user of that tenant, so
it cannot vary with the user who triggered the lookup: the source is handed
tenant IDs and never sees the caller. One consequence: a Zitadel user deleted
or deactivated individually is *not* revoked by this axis.

A token that introspects without a `resource_owner` is still covered, because
the claim names its tenant. Zitadel emits `resource_owner` only when the
token's scopes ask for it, so an empty one is an OIDC client
misconfiguration; it is logged at `Error` once per introspection, not once per
request.

A token carrying **neither** claim is refused at introspection (`401`). It is
also denied by the liveness axis, which is the second, independent closure:
every such token derives the same synthetic tenant ID (`ComputeUUID(issuer,
"")`) and no row carries it, so the axis reports it not served. A malformed
`pyck_tenant_id` is treated as absent and takes the same two refusals.

What bounds the first case is pyck's own token cache, not the token's expiry:
`Authenticate` short-circuits introspection on a cache hit and only the
liveness check runs on that path, so a user deleted or deactivated in
Zitadel out of band keeps access for up to `PYCK_ZITADEL_PAT_CACHE_TTL`
(default `1h`). After that the entry expires, introspection runs again, and
Zitadel reports the token inactive. An operator revoking a compromised account
must either wait out that TTL or restart the services holding the entry. The
axis that would close this — a per-user liveness verdict under the
`zitadel:users:<sub>` key the liveness package already reserves — is tracked
as [#1433](https://github.com/pyck-ai/pyck/issues/1433).

`disabledTenants` is the only liveness query, and it is part of the subgraph
contract: whenever the gateway, the composed supergraph and the calling
services disagree on it, the router rejects the query and the lookup reports
an infrastructure fault. A fault is never a revocation and is never cached,
but a tenant that cannot be verified is not served, so liveness fails
**closed** for uncached tenants for the length of the disagreement, and every
request of every replica on the wrong side opens its own gateway round-trip,
collapsed per acting set per round-trip.

It is additive to the supergraph, so **deploy the composed supergraph before
the services**; this order is availability-critical, not just tidy: a
replica calling the query before the router knows it denies every uncached
tenant until the gateway catches up.

**System callers** (`ROLE_SYSTEM` service tokens, `PYCK_SERVICE_TOKEN`) skip
the liveness axis. They are how tenants get disabled and restored
(`deleteTenant` / `restoreTenant` name their target in the input), and their
identity is validated by the system-role check at introspection.

`restoreTenant` and `setTenantExpiry` require a system caller in the
resolver itself (a system caller passes every role check, so the
`ROLE_ADMIN` gate on those two resolves the target and nothing more).
Tenant registration grants the tenant's own admin `ROLE_ADMIN`, and the
asymmetry is the soft-delete design: a tenant may disable itself
(`deleteTenant` admits its own admin), only an operator brings it back,
and the expiry is the operator's deadline; a tenant admin able to move it
would outlive the operator's decision on its own. A cross-tenant admin is
refused the same way, which matters if the header rule for disabled
tenants is ever relaxed from denial to filtering. The user-projection
mutations (`createUser`, `updateUser`, `deleteUser`) carry the same guard
for a different reason: the `users` table is a zitadel-sync-owned
projection whose `idp_id` is what role assignment resolves its Zitadel
target from.

Tenants registered before the admin grant existed keep a writer-only admin
and an org grant without the `admin` role, so they keep exactly the access
they had: nothing widens them. Nothing on the request path does either:
`assignRoles` rejects every non-service role before it reaches Zitadel
(`ErrNotAServiceRole`), so it only ever widens the menu with the *service*
role being assigned, never with `admin`.

Where such a tenant's admin needs the admin-gated surfaces (`assignRoles`,
`removeRoles`, `userServiceRoles`), an operator adds `admin` in Zitadel, in
this order: to the org's project grant (the menu caps what any of its
users may hold), then to the admin's own user authorization. Both are
full-replace updates against APIs with no compare-and-set: send the union
of the current roles and `admin`, never `admin` alone.

## Access matrix

Caller: user homed in tenant **A**, with a role grant into tenant **B**.
Rows are the header sent, columns the state of B.

| `X-Pyck-Tenant-Id` | B active | B disabled | B restored |
|---|---|---|---|
| `B` | 200 | **403** | 200 (next request) |
| `A,B` | 200 | **403** | 200 |
| `all,B` | 200 | **403** (explicit part wins) | 200 |
| `all` | 200, sees A + B | 200, **B filtered out** | 200, sees A + B |
| `all`, every tenant the caller may see disabled | — | **403** (an empty acting set would only surface as a misleading downstream error) | — |
| *(absent)* | same as `all` | same as `all` | same as `all` |
| `A` | 200 | 200 (unaffected) | 200 |
| `C` (no grant) | 400 | 400 (state never looked up) | 400 |
| home tenant **A** disabled | — | **403** at the row flip (home tenant always checked), **401** once Zitadel deactivates the org | 200 |
| system token, `B` | 200 | 200 (bypass; needed for `restoreTenant`) | 200 |
| `all`, mutation, caller granted A + B only | refused (two tenants, no unambiguous target) | 200 on **A** (B filtered out leaves one target) | refused again |

The same matrix holds on every service's GraphQL path: management checks its
own `tenant` table in-process, the other six ask management through the
gateway (`disabledTenants` query, system-only). Both are exercised end-to-end
by `tests/integration/tests/disabled-tenant-access`. See "Surfaces covered"
below for the two entry points that are not.

## Revocation window and caching

Both post-authentication checks read and write **one liveness cache per
service** (`backend/common/liveness`), constructed in `main` and injected
into the auth provider, the tenant middleware and the lifecycle subscriber.
Keys are URNs namespaced by the system that owns the truth. Only one kind
exists today (every verdict comes from the tenant table) and the namespace
is what lets a principal owned by another system (users, say) be added later
without two verdicts about the same subject sharing an entry:

| key | live means | source on miss | written by events |
|---|---|---|---|
| `pyck:tenants:<uuid>` | a tenant row exists, is not soft-deleted, and its registration finished | management tenant table (`disabledTenants`, batched per request; both signs cached) | every tenant row event (create / update / soft delete): **evicted** |

Events only evict, never assert state. That is deliberate: the outbox drainer
orders events within one transaction group only, and a failed publish is
retried later, so a delayed restore event can land after a later disable
event; asserting state from events would then serve the wrong verdict for a
full TTL, while an eviction at worst costs one extra lookup and always
converges on the source's truth. Every tenant row event evicts, not only the
two lifecycle transitions: an unneeded eviction costs one extra lookup.
Entries expire after `PYCK_LIVENESS_CACHE_TTL` (default 1 min); lookups are
generation-guarded (a source answer that overlapped an event is discarded);
duplicates are idempotent; a lost event heals at the TTL — in either
direction. Every disable path writes one tenant row at a time and emits that
tenant's event — `deleteTenant` / `restoreTenant`, the expiry sweep and the
Zitadel sync's tenant reconcile — so all of them evict instantly. Writes made
with events suppressed (tenant registration and its compensation) evict
nothing and are picked up at the TTL.

Steady state costs no round-trip whether a tenant is enabled or disabled;
only a never-seen, evicted or expired key reaches its source. On `deleteTenant`
the tenant CRUD event fans out over NATS (one ephemeral consumer per
replica); measured locally, header B is denied ≈200 ms (management) / ≈4 ms
(inventory) after the mutation returns, and re-admitted just as fast after
`restoreTenant`.

## Infrastructure faults

If the liveness source fails (management unreachable, gateway 5xx, decode
fault), a tenant with a **cached verdict keeps it** (a known-live tenant
stays served, a known-disabled tenant stays denied) but a tenant with no
verdict on that replica (never seen, evicted, expired) is **denied for as
long as the outage lasts**: a tenant nobody can verify is not served, because
admitting it would re-open a deleted tenant's data for the length of the
outage. The same rule holds on the auth-path check (the caller's home
tenant): the request is refused, but the cached token is **not** evicted;
the fault is transient, and evicting would send every request of the outage
back through Zitadel introspection and delay recovery.

The operational consequence is deliberate and worth stating plainly: **a
management outage longer than `PYCK_LIVENESS_CACHE_TTL` (default 1 min) is a
full stop for tenant traffic**; cached verdicts carry the fleet for one TTL,
then every surface denies until the source answers again.

Because the NATS callout also denies during a fault, **every NATS client
must treat auth errors as retryable**: `nats.go`'s default behaviour closes
the connection permanently after the same auth error twice and caps
reconnects at 60, which would turn a brief outage into a dead client that
never recovers. pyck's own `events.NewNatsClient` sets
`IgnoreAuthErrorAbort` and unlimited reconnects; external tenant clients
must do the equivalent, and must expect their minted credential to expire
(~5 min, jittered) with a reconnect that re-passes the callout. Nothing is cached
from a fault, so service resumes with the first successful lookup; system
callers bypass the axis entirely, so platform tooling keeps working
throughout. A hanging (not refused) source call, a black-holed socket
rather than a refused connection, is bounded by the liveness cache itself:
it runs on a detached context with its own 10 s deadline (`sourceTimeout` in
`backend/common/liveness`), after which it is treated like any other fault.
The bound belongs to the cache because the call is shared: it outlives the
request that started it, and management's own two sources — a raw gRPC call
to Zitadel, an Ent query with no `statement_timeout` — carry no deadline of
their own.

Both fault paths report the failure once per minute per replica
(`log.FaultThrottle`, `log.FaultLogWindow`) and log the repetitions at
`Debug`, with the suppressed count on the next `Error` line. Both run per
request and their source is shared by the whole fleet, so an unthrottled line
would be written at the fleet's request rate for the length of the outage and
would bury the one line naming its cause.

### Rollout ordering

A freshly started replica has an empty cache, so until its verdicts warm
every tenant it serves depends on the source answering: if the liveness
source does not (most visibly when a management/gateway rollout that
changes the `disabledTenants` contract lands **after** the consumer
services) that replica denies every tenant request until the source
recovers. Roll out (and roll back) management and the gateway before the
services that consume the query, and read a fleet-wide stream of
`disabled-tenant lookup failed` lines as the signal that tenant traffic is
being refused for want of verdicts.

## Surfaces covered

The matrix applies to the GraphQL HTTP path of all seven services — every
request that passes `tenant.Middleware`, and to the two non-HTTP entry
points, which read the same per-service verdict cache:

- the NATS auth-callout in management (`backend/common/nats/auth_handler.go`)
  refuses a connection whose selection is not exactly one tenant or whose
  user holds no role in it (`resolveTenant` in
  `backend/common/nats/tenant_access.go`), or whose tenant no live row
  backs. The checks run at connect time, so minted tenant-user credentials
  expire after about five minutes (`userJWTTTL` in `auth_handler.go`,
  jittered by up to a minute either way so a cohort admitted together does
  not re-expire in lockstep): the server disconnects the client at expiry,
  the reconnect re-passes the callout, and a disable therefore reaches
  standing connections within that window. System-user credentials do not
  expire: their access is not liveness-derived;
- the Temporal claim mapper (`backend/temporal/authz/claimmapper.go`) drops
  the namespace claim of every tenant no live row backs, on every request,
  from the same per-service cache; the user's home tenant is additionally
  gated inside `Authenticate`. System users bypass the filter: their
  access is not derived from per-tenant grants.

## Verifying locally

```bash
task up
cd tests/integration && source ./scripts/envrc.sh && \
  go test -tags=integration -v -count=1 ./tests/disabled-tenant-access/...
```

The suite provisions two fresh tenants and a cross-tenant user, disables B,
and asserts every row of the matrix from both the management and inventory
vantage points; it then restores B (re-admission, B back in `all`) and
disables it a second time. Every denial and the re-admission must land inside
half the verdict TTL, which proves they were driven by the NATS evictions
rather than by the cached verdict merely expiring — the second cycle proves
eviction also hits a verdict re-cached after a restore. TTL expiry itself (the
lost-event fallback) is covered by unit tests in `backend/common/liveness`, not
end-to-end.
