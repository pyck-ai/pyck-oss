# Tenant registration

`registerTenant` (management) provisions a tenant DB-first and
event-driven. The resolver validates the input, chooses the identity
(`authn.NewTenantIdentity`: a random Zitadel org ID and
`tenant.id = ComputeUUID(audience, orgID)`), hashes the admin password with
bcrypt, puts the tenant's event stream `<stream>-<tenantID>` (the row's
create event is published there) and commits a `status=pending` tenant row
in its own transaction.
The row carries the admin account (`registration`: username, e-mail, names,
password hash) in a Sensitive JSON column that is absent from the GraphQL
type and from every outbox/NATS payload. The resolver then waits for the
registration run under `register-tenant-<tenantID>`.

The row's outbox create event reaches the tenant-lifecycle trigger
(`events/tenants`), which starts `RegisterTenantDbFirstWorkflow` under that
ID (reuse policy `ALLOW_DUPLICATE_FAILED_ONLY`: a redelivered event during
or after a successful run is dropped, a retry after a failed run stays
possible). The workflow loads the row and, if it is still pending, creates
the Zitadel org under the pre-chosen ID, imports the admin user with the
hashed password (read from the row inside the activity, so the hash never
enters workflow history), sets it as org owner, grants the project and the
user, creates the default datatypes and the Temporal namespace, writes the
org metadata, triggers the user sync, provisions (for `pyck-go` tenants)
the worker and finally flips the row to `status=done`, clearing
`registration`. The worker is the last provisioning step: it starts as
soon as its deployment exists and carries a long-lived credential, so any
step that can still fail runs before it.
A row that is not pending when the run starts (replayed event, finished or
failed registration) is a logged no-op.

Any failing step marks the row `status=error` and ends the run with that
step's error; nothing is compensated. The resolver re-reads the row when
the run closes: `done` → the tenant, `error` → an error naming the failing
activity and a fixed text for its application error type
(`registertenant.FailureMessage`): the types only the caller's own input
produces (`organization-name-taken`, `organization-invalid`,
`admin-account-invalid`, `org-metadata-invalid`) get their own text, and
`admin-username-taken` is answered as `admin-account-invalid` (usernames
are unique across the Zitadel instance, so an answer of its own would
tell anyone whether a username exists; the log keeps the real type);
every other type (the operator-side
`org-metadata-rejected` (a missing grant, an unsupported API) and
`admin-hash-unsupported` (a Zitadel that does not accept pyck's bcrypt
hash, Zitadel error `USER-JDk4t`) among them) and every timeout the
generic `provisioning failed`. No
dependency text is ever part of the answer, not Temporal's (workflow ID,
run ID, worker identity), not the application error's message (request
paths, secret names, response bodies), not the wait's transport error,
because `registerTenant` is anonymous; the full chain is logged. The re-read goes to the reader pool, so a row a lagging replica
still reports `pending` is read again every `registrationPoll` until it
changes or the wait runs out.
Anything else (the run has not appeared or closed within the resolver's
wait, Temporal unreachable, request cancelled) is an error stating the
outcome is unknown while the row stays `pending` and the run finishes on
its own; the client verifies by name once it has. A failure of pyck's own
database or password hash outside the run (the name pre-flight, the
pending row's transaction, the re-read after the run) is answered with the
fixed `tenant registration could not be processed` and logged; it does not
claim the row was or was not written, so the client verifies by name here
too.

The resolver's wait (`registrationWait`, 25 s) is sized under the
gateway's 30 s per-subgraph request timeout (Apollo Router's default, set
explicitly in the deployment's router configuration): past that bound the
router drops the request and the unknown-outcome error would never reach
the client. A healthy run finishes in about a second; a run that has to
retry a step whose dependency is down keeps retrying past this wait (a
step is bounded by its own activity budget, not by the resolver's) and is
reported as outcome unknown while it settles the row on its own. The wait
holds no transaction and no pooled connection: `registerTenant` runs
under `gqltx.WithoutTransaction`, the pending row is committed in its own
short transaction before the wait, and the re-reads after it are single
queries. How many waits a replica holds at once is bounded by the
admission cap (`PYCK_REGISTER_TENANT_MAX_INFLIGHT`).

A `registerTenant` request carrying any `X-Pyck-Feature` header is refused
before anything is validated or written: the features change how the row's
own transaction behaves (`suppressevents` skips the outbox write, so no run
would ever start and the row would stay `pending`).

The tenant name and the admin account are validated at the resolver the
way Zitadel validates them at org creation and user import (non-blank, at
most 200 characters each, the e-mail a single plain address): Zitadel
checks them only after the org exists, so a value it rejects would
otherwise end as an `error` row with an ACTIVE org holding the name. The
e-mail is also held to Zitadel's host-name rule (protoc-gen-validate
`email: true`): at most 254 bytes, a local part of at most 64, a host
whose labels are 1-63 ASCII letters, digits and inner hyphens, so IDN domains, IP literals (`a@[127.0.0.1]`) and underscores
are refused. Every string field but the password is also refused with
leading or trailing whitespace (Unicode spaces such as NBSP included) or a
control character (NUL included): Zitadel trims names before it stores
them, so " Acme" would pass the name index next to a live "Acme" and
collide inside Zitadel, and Postgres refuses NUL in text. The password is
only refused for a control character or for not being in NFC (see below):
Zitadel stores its hash and trims nothing, so surrounding and inner spaces
are part of the password. A
rejection Zitadel still produces (`InvalidArgument`) is classified
non-retryable in `CreateTenantActivity` and `CreateZitadelUserActivity`.

Every GraphQL `String` input is NFC-normalized by the scalar
(`backend/common/gqlscalar`), so exact-match filters find a name whether
the client sent it composed or decomposed. `adminPassword` is the
exception: it is typed `Password` (mapped to `gqlscalar.RawString`),
which keeps the bytes the client sent, and nothing between the resolver,
bcrypt and Zitadel's import rewrites them. Zitadel verifies the login
bytes against that hash and never normalizes, and keyboards on macOS,
Linux and Windows send NFC. So `registerTenant` accepts NFC passwords
only: one that arrives decomposed (`o` + U+0308, e.g. pasted from an NFD
source) is refused with `adminPassword is not in composed Unicode form
(NFC)` rather than normalized, since it would register bytes the admin
cannot type at the login. The stored hash is therefore always of NFC
bytes, exactly as sent.

The `data` map is bounded at the resolver (32 keys of at most 200
characters, scalar non-empty values, 16 KiB serialized): the whole map is
mirrored onto the org in one Zitadel metadata write, so the key count does
not decide whether an attempt fits the run's 30 s activity attempt, and a
value Zitadel rejects ends as an `error` row with an org.

The row's `registration` column holds the bcrypt hash until the run
finishes with `done`, or until the row is retired: an `error` row keeps it
for the operator's re-run, and every soft delete (`deleteTenant`, the
expiry sweep) clears it. `Sensitive` keeps it out of the outbox and NATS
payloads, not out of the Ent debug driver: with `DB_DEBUG` on, the INSERT is logged with its
arguments, hash included. Do not run shared environments with `DB_DEBUG`
while registrations happen.

Only `done` rows take part in zitadel-sync, expiry and tenant-reconcile;
`pending` and `error` rows are excluded from all three. The lifecycle
trigger dispatches `DisableTenantWorkflow` and `RestoreTenantWorkflow` for
rows of every status: a `pending` row may already own its org (the run
creates it before the status flip), and both workflows tolerate an org
that was never created. Tenant names are compared the way Zitadel compares
org names, by Go's `strings.ToLower` ("Acme" and "ACME", "Ⓐcme" and "ⓐcme"
are the same name), and are held by every row, whatever its status and
whether or not it is soft-deleted: the column `name_key` holds the folded
name (`types.TenantNameKey`, set by a schema hook on every write of
`name`), the unique index `tenant_name_key` on it covers all rows, and the
resolver's pre-flight checks the same set. A retired tenant keeps its
name, so `restoreTenant` can always bring it back. pyck may be built with
newer Unicode tables than Zitadel; case pairs are only ever added, so the
key can refuse a name an older Zitadel would accept, never accept one it
refuses.

## Tenant liveness after an out-of-band Zitadel org deletion

The tenant row is the only liveness truth. zitadel-sync projects users from
Zitadel into the DB per tenant, but tenants flow one way (DB to Zitadel at
registration) so an org deleted out of band (a cleanup job, a manual
delete in the Zitadel console) does not soft-delete the tenant, and an org
renamed or given metadata in Zitadel is not mirrored back either. The
symptom is a tenant whose org lookups fail: the per-tenant sync probes the
org before listing its users (Zitadel answers listings for a removed org
with empty results, which would otherwise wipe the projection), warns
"tenant org no longer exists; user sync skipped until an operator runs
deleteTenant" each cycle and leaves the last projected users in place, and
the tenant-reconcile sweep does not
treat the pair (active row, deleted org) as drift. The remedy is
the `deleteTenant` mutation: it soft-deletes the row and dispatches
`DisableTenantWorkflow`, whose `DeactivateZitadelOrgActivity` treats
`NotFound` as done.

## Failure handling

| Failure | Row | What exists in Zitadel | Next step |
|---|---|---|---|
| Invalid input, name taken (case-insensitively, by any row, retired ones included), an `X-Pyck-Feature` header | none | nothing | client fixes the request |
| Many concurrent registrations | one `pending` row per accepted call | per run: nothing yet, or everything up to its failing step | `PYCK_REGISTER_TENANT_MAX_INFLIGHT` (default 8) caps how many run at once per replica; the (cap+1)-th is refused immediately with "too many concurrent executions of this operation" and costs nothing. The cap is a concurrency bound, not a rate limit: nothing in front of the service limits the rate of anonymous calls (an admission cap for the other mutations with this shape is #1430). The registration wait itself holds no writer connection: `registerTenant` is registered with `gqltx.WithoutTransaction` |
| Event stream cannot be put or sourced | none (the stream is deleted again, best effort) | nothing | client retries |
| Pending row cannot be committed | none (its stream is deleted again, best effort) | nothing | client retries |
| Create event never starts a run (Temporal unreachable past the trigger's NAK budget (each failed start is redelivered after 2, 5, 15, 30 s, so at the default `PYCK_TENANT_EVENT_MAX_REDELIVER=5` an outage of about 52 s when Temporal refuses at once, up to about 102 s when every start runs into the 10 s `PYCK_TENANT_EVENT_TEMPORAL_CALL_TIMEOUT`, and the last delivery logs "tenant event exhausted its redeliveries" at Error) or event ACKed by a pre-rollout replica) | `pending` | nothing | operator starts the run or retires the row (below); the resolver reports an unknown outcome |
| Run cannot complete a workflow task (panic in workflow code, non-deterministic replay after a deploy) | `pending` | whatever was created before | the run closes `TIMED_OUT` after `registertenant.ExecutionTimeout` (85 min) without marking the row; operator retries, or retires the row at any time (a run still open notices the deleted row itself) |
| A step's dependency is down (Zitadel restarting, worker-api 5xx, database connection lost) | `pending` while it retries, then `error` | whatever was created before | the step retries for up to 5 minutes (1 minute for the worker identity and the sync trigger); a dependency back inside that budget completes the registration, one that is not marks the row `error` |
| `CreateTenantActivity` fails (name held by a stale org, Zitadel down past retries) | `error` | nothing, or the org | operator retries or retires |
| `CreateZitadelUserActivity` cannot import the admin (usernames are unique across the whole instance under Zitadel's default domain policy, so a user of another org holding it answers `AlreadyExists`) | `error` | the org | operator retires, or retries with another username (below) |
| Any later step fails | `error` | the org and everything created before the failing step (user, grants, namespace, worker, metadata) | operator retries or retires |
| Final status flip fails | `pending` | everything | operator retries (`SetTenantStatusActivity` is idempotent) or retires |
| Resolver loses the result (Temporal unreachable, request cancelled, `registrationWait` of 25 s exceeded) | whatever the run leaves | whatever the run leaves | the run finishes on its own; the client verifies by name |

No registration deletes anything: an `error` row keeps its org, its admin
user and its name until an operator acts, which replaces the automatic
rollback of earlier designs (deleted org, freed name) with an explicit
operator decision.

A step is refused or retried, never both: a request the dependency
answers on its own terms (a name already taken, an account or a metadata
value it rejects, a row that is not pending) fails the run on the first
attempt, while a fault (a restart, a 5xx, a lost database connection) is
retried until the step's `ScheduleToCloseTimeout` is spent. The steps that
act on an object an earlier step of the same run created keep `NotFound`
and `FailedPrecondition` retryable as well, before classifying: Zitadel
validates against projections it updates asynchronously and Temporal
against a namespace cache, so both answer "not there" for an object their
own eventstore has already accepted, and reading that as a refusal would
fail a registration over its own writes. That budget is
5 minutes per provisioning step, 1 minute for the worker identity (every
attempt of it mints a token nothing revokes) and 1 minute for the
best-effort sync trigger, so a run whose dependency never comes back
stays open for up to ~70 minutes before its row is marked `error`, and one
that is still RUNNING after 40 minutes is retrying, not wedged. The
trigger starts every run with `registertenant.ExecutionTimeout` (every
activity a run can schedule at the provisioning budget, plus one budget
of margin: 90 min): a run that cannot complete a workflow task at all
closes `TIMED_OUT` instead of holding the workflow ID against the
operator's retry, leaving the row `pending`.

### Retrying a failed registration

Every provisioning step is idempotent under re-run: the org is created
by ID, an existing admin user, org membership, project grant and user
grant are looked up, default datatypes already present are skipped, the
Temporal namespace and search attributes tolerate "already exists", the
worker secret is overwritten and worker-api treats an existing deployment
as success. The one exception is `ProvisionTenantWorkerIdentityActivity` of a
`pyck-go` tenant, which mints a fresh PAT on every *attempt* and pushes it
to the secret store in the same call; the previous tokens stay valid until
they expire, which is why that step has the shorter 1-minute budget.

1. Fix the cause (for example remove the stale Zitadel org holding the
   name, or for an admin username another org already holds give the row
   another username:
   `UPDATE management.tenants SET registration = registration || '{"adminUsername": "<new>"}' WHERE id = '<tenantID>' AND status = 'error';`).
2. Reset the row so the run finds it pending:
   `UPDATE management.tenants SET status = 'pending' WHERE id = '<tenantID>' AND status = 'error';`
3. Start the run under the same ID:
   `temporal workflow start --type RegisterTenantDbFirstWorkflow --task-queue pyck-management-task-queue --workflow-id register-tenant-<tenantID> --id-reuse-policy AllowDuplicate --execution-timeout 5400s --input '{"TenantID":"<tenantID>"}'`
   `--execution-timeout 5400s` mirrors the trigger's `ExecutionTimeout`
   (90 min; keep the two in sync): without it the operator-started run
   records no bound, `affordableBudget` caps nothing, and a wedged run
   (a workflow-code panic, a non-deterministic replay after a deploy)
   would retry forever, holding the workflow ID against every further
   start and the row in `pending`.
   The reuse policy belongs to the start request, not to the ID: the
   trigger's `ALLOW_DUPLICATE_FAILED_ONLY` only governs its own starts,
   so an operator start is admitted after any closed run: FAILED, or
   COMPLETED as a no-op because an earlier run found the row not
   pending (`--id-reuse-policy AllowDuplicate` is the CLI default; it is
   spelled out so the command does not depend on that).
4. Watch it reach `status=done`. The row's `registration` column is
   cleared by the final step; until then it holds the password hash.

A row stuck in `pending` with no run under its ID (the consumer never
started it) only needs step 3.

A deploy that widens the roles a registration grants hits the runs already
in flight, and what an operator sees afterwards depends on which versioning
mode the workers run in. Temporal does not flag either case: its replay
check compares the command sequence, not activity inputs.

**Unversioned** (local stacks, and any worker whose build ID resolves to
`(devel)` with `PYCK_WORKER_REQUIRE_BUILD_ID=false`): activities of one run
are served by whichever worker picks them up, so a run that already executed
`AddProjectGrantActivity` under the old build reaches `AddUserGrantActivity`
under the new one, asks for a role the org's menu does not carry yet and
fails with `Errors.Project.Role.NotFound`. The symptom is a burst of `error`
rows lasting the duration of one registration, and the re-run above repairs
them: the grant steps extend a stale menu on their AlreadyExists paths.

**Versioned** (dev, test and prod, where deploys set a build ID and
`PYCK_WORKER_REQUIRE_BUILD_ID=true`): workers run under Worker Deployment
Versioning with `DefaultVersioningBehavior: Pinned`
(`backend/common/workflow/versioning.go`), so an execution finishes on the
version it started on. The in-flight run completes on the *old* build with
the *old* role set: no error row, nothing to re-run (`SetTenantStatusActivity`
marked the row `done` and cleared its `registration` data) and a tenant
whose admin silently lacks the new role. Do not read the absence of `error`
rows as a clean deploy.

Nothing widens a tenant whose registration finished before a role was
added, in either mode. Where one needs the role, an operator adds it in
Zitadel in two steps, in this order: to the org's project grant (the menu
caps what any of its users may hold), then to the user's authorization.

### Retiring a registration

`deleteTenant(input: {tenantId})` (the service token or the tenant's own
admin) retires a row in any status. It soft-deletes the row, which clears
its `registration` data, and its delete event dispatches
`DisableTenantWorkflow`, which deactivates the org if it exists
(`DeactivateZitadelOrgActivity` treats `NotFound` as done). A retired row
cannot be re-run, and `restoreTenant` refuses one whose registration did
not finish (`tenant registration did not finish`): a row only comes back
when its status is `done`. The row keeps its name, so no other
registration can take it, in any case. Nothing removes a tenant row (the
expiry sweep only soft-deletes), so a retired name stays taken for good,
including the name of an anonymous caller's `error` row.

Freeing a name is an operator step in both systems: rename the retired
row (its `name_key` too, which is what the name is unique by), and delete (or rename) the deactivated Zitadel org that still carries
the name, because Zitadel refuses a second org of the same name
(case-insensitively) and the next registration would end as an `error`
row. Do not restore a renamed row; its org still has the old name.

```bash
psql -c "UPDATE management.tenants \
         SET name = name || '-retired-' || id, name_key = name_key || '-retired-' || id \
         WHERE id = '<tenantID>' AND deleted_at IS NOT NULL"
```

For a `pending` row, `deleteTenant` leaves the open
`register-tenant-<tenantID>` run alone, so the delete also works while
Temporal is down. The run notices the delete itself: the admin import and
the status flips refuse a soft-deleted row, and a `pyck-go` run re-reads
the row before the worker. A status flip that finds the row deleted, or a
re-check that finds it gone, makes the run deactivate the org the row
names (`DeactivateRetiredOrganizationActivity`, which refuses a live row)
and end. `DisableTenantWorkflow` runs once, on the delete event; if that
is before the run creates the org, it finds none and counts that as done,
and the run's own deactivation covers the org. Two leftovers remain until
an operator removes them: a worker a `pyck-go` run deployed between its
re-check and the done flip, with its PAT, and an org whose deactivation
failed past its budget, which stays `ACTIVE` (liveness denies the tenant,
the row being soft-deleted). Find them by listing soft-deleted rows that were retired while
`pending` and checking their org in Zitadel:

```bash
psql -c "SELECT id, name, idp_org_ref FROM management.tenants \
         WHERE status = 'pending' AND deleted_at IS NOT NULL"
```

An org of one of these ids that Zitadel reports `ACTIVE` (or a worker
deployment for the tenant) is such a leftover.

## Adding a search attribute to an existing namespace

Registration installs the platform search attributes on a tenant's
namespace, and every service re-checks them when it first uses that
namespace (`DefaultClientFactory.addSearchAttributes`). Adding one to a
namespace out of band goes through Temporal's **internal frontend** only:
the claim-mapped public frontend (7233) disables
`OperatorService/AddSearchAttributes` and `RemoveSearchAttributes`
(`backend/temporal/authz`), because a tenant admin's PAT carries a
namespace `RoleAdmin` claim and would otherwise be able to unregister the
platform's attributes from its own namespace.

```bash
# cluster only: the internal frontend has no ingress
kubectl port-forward svc/pyck-temporal-internal-frontend 7236:7236

# local stack: 7236 is already published (config/compose/third-party/temporal.yaml)
temporal --address localhost:7236 operator search-attribute create \
  --namespace <tenant> --name <Name> --type Keyword
```

Against the public frontend the same command answers "API is disabled".
Services must keep pointing `PYCK_TEMPORAL_URL` at the internal frontend
for the same reason: a service that cannot register the attributes fails
to build its Temporal client for the namespace at all.

## Changing the registration workflow

`RegisterTenantDbFirstWorkflow`'s activity sequence is part of its replay
contract and has no `workflow.GetVersion` gate. Management sets no
`WorkflowPanicPolicy`, so the SDK default (`BlockWorkflow`) applies: an
execution whose history records a sequence the new binary does not
produce neither fails nor progresses. It blocks, holding its workflow ID
and its row in `pending`, until `registertenant.ExecutionTimeout` closes
it `TIMED_OUT` without the status flip. A change to the sequence
therefore either adds a `GetVersion` gate or is deployed with no
registration in flight: no `pending` row and no running
`RegisterTenantDbFirstWorkflow`.

A change to an activity budget needs neither: `WorkflowExecutionTimeout`
is fixed by the start request while activity options are re-read from
the new worker's code on every workflow task, so the workflow caps every
step at the share of its own recorded bound one activity may spend
(`affordableBudget`). A run started by an older build keeps that build's
budgets and cannot close `TIMED_OUT` without marking its row.

The tenant-lifecycle trigger reads the durable consumer
`management-tenant-lifecycle-trigger-v2`, shared by every management
replica. A durable's subject filter is not versioned: while old and new
pods share one durable, an event the new pods handle can be delivered to
an old pod that ACKs it unhandled. A change to the filter therefore moves
to a new durable name, which only the new pods read; during the overlap
both durables dispatch update and delete events, which is idempotent by
workflow ID (`TERMINATE_EXISTING` for the lifecycle workflows,
`WorkflowExecutionAlreadyStarted` treated as success for a registration).
The durable is created with `DeliverNew`, so it starts at the tip of the
stream: the handler derives each lifecycle transition from the event
payload, and replaying the stream's retained history would re-dispatch
every disable and restore the platform has ever performed. A create event
published before the durable exists is never delivered.

## NATS access of tenant clients

The auth callout places every token connection in the account
`PYCK_NATS_TENANT_ACCOUNT_NAME` (`pyck-tenants`), not in `pyck`, which
holds the streams and the platform services. The tenant account has no
JetStream of its own and imports from `pyck` only the JetStream API
calls tenant clients are allowed, acks and the event subjects
(`config/nats/nats.conf`). The server answers a request on its reply
subject without checking that subject against the client's
permissions; in the tenant account the answer stays there, so a client
cannot have it stored in another tenant's stream or in the system
stream. Management refuses to start when
`PYCK_NATS_TENANT_ACCOUNT_NAME` names the account its own NATS
connection is in, so the tenant account can never be the services'
account, and when the NATS server has no system account that answers
`$SYS.REQ.USER.INFO`, which that check asks.

All tenant clients share that account, so a client may subscribe to
nothing but its own reply inbox, `_INBOX.<tenantID>.>`, and must connect
with `nats.CustomInboxPrefix("_INBOX.<tenantID>")` (the id in lower case).
On the default prefix every request, JetStream call and fetch times out.
Any other subject of the account would also receive answers that other
tenants' clients aim at it, so there are no core subscriptions: a client
reads its events through a pull consumer on its own stream, under one of
its ten consumer names (`<tenantID>--one` to `<tenantID>--ten`).
