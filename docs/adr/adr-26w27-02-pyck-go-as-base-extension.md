# ADR-26W27-02: pyck-go as a Pure Extension on the Base, Driven by Lifecycle Hooks

> **🟡 Status: Proposed**
>
> 2026-07-01. Directional ADR: it fixes the target architecture and the mechanism, and leaves concrete wire details (event subjects, the `ExtensionSetupComplete` mutation and `extensionStatus` shape, the stuck-tenant escape hatch) to follow-on work. Depends on [ADR-26W27-01](./adr-26w27-01-tenant-extensions-field.md): the `extensions` set is the tenant-side registry that tells the base which extension initialisers to invoke.
>
> Amended 2026-09-14. The DB-first tenant registration work for #1337 built the base's side of sections 1.3, 1.4 and 2.2: the registration event and a tenant-level completion barrier. Those sections now describe the code, and section 2.6 records where the shipped barrier and role model differ from the lock as proposed. The extension hook, the `ExtensionSetupComplete` mutation, the `isSetupPending` lock and the relocation of pyck-go code are still open.

## 1 Context

pyck-go is meant to be an extension on top of the base pyck platform, but the code is split across two repos in a way that does not yet reflect that. Pyck-go-specific logic lives inside `pyck-ai/pyck` (the base), while the extension's own workflows and UI live in `pyck-ai/pyck-go`. The long-term goal is that **all pyck-go code lives in `pyck-ai/pyck-go`** and the base carries no knowledge of pyck-go specifically, only a general extension mechanism that extensions plug into.

### 1.1 pyck-go knowledge currently embedded in the base

- `backend/management/workflows/register-tenant/workflow.go` branches on the resolved flavour and, only for pyck-go, calls `deployTenantWorker`, which provisions per-tenant Temporal worker infrastructure in Kubernetes (shared workers namespace, a dedicated Zitadel service user and API key stored as a secret, a per-tenant Temporal `Connection`, and a `WorkerDeployment`).
- The base no longer carries pyck-go worker settings itself (the `FlavourGoWorker*` config is gone): `deployTenantWorker` asks the worker API for a deployment named for the `pyckGo` extension, and image, replicas and environment live on that side. The base still decides *whether* to ask, and that decision is the branch this ADR removes.
- The tenant-settings schema and the frontend feature parse both hard-code pyck-go concepts (addressed for the flag itself in [ADR-26W27-01](./adr-26w27-01-tenant-extensions-field.md)).

The base therefore has a hard-coded `if pyck-go then do pyck-go things` seam. Every new extension would add another such branch. That does not scale and it couples the base's release to extension-specific logic.

### 1.2 How the extension hooks in today

pyck-go currently self-starts its onboarding rather than being invited by the base. `main.go` in the pyck-go repo boot-starts onboarding via `SignalWithStartWorkflow` (`start-signal`) with idempotency guards that infer prior completion from the presence of stored config (for example, a saved Sendcloud config implies `OnboardingSendcloud` already completed). This works, but it is pull-based and boot-triggered: the extension polls or self-initialises instead of reacting to a base tenant-lifecycle event, and the "should this tenant even get pyck-go" decision is made by deploying a pyck-go worker at all, not by a clean signal from the base.

### 1.3 The base already has the ingredients for a hook

The base emits domain events through a transactional outbox: entity mutations are stamped into `entityeventsoutbox` by a `MutationEventHook`, and those events are published onto NATS with JetStream. Tenant registration already uses this path for itself: the `registerTenant` resolver inserts the tenant row at `status = pending` in its own transaction, with a transaction ID so the insert lands an outbox row, and management's own trigger consumer starts the registration workflow from the resulting `create` event. The base also runs Temporal. So the raw materials for an event-driven extension point (durable event on tenant registration, delivered to an extension that starts a Temporal workflow) exist and are exercised; they are simply not exposed as a general hook.

### 1.4 Forces

- The base must not depend on any specific extension. Adding, removing, or changing pyck-go must not require a base release.
- Tenant initialisation for an extension may need to run before, instead of, or after the base's default behaviour for a step. A pure "notify after the fact" hook is insufficient; the extension must be able to suppress or replace a base default in some cases.
- The mechanism must be durable and idempotent. Tenant registration does not roll back: a failed run leaves the row at `status = error` together with whatever Zitadel objects it had created, and a retry of the same tenant resumes idempotently (pre-chosen organization and user IDs, one workflow per tenant started with `ALLOW_DUPLICATE_FAILED_ONLY`). Extension initialisation triggered from it must survive retries and redeliveries the same way, without double-provisioning.
- Ordering and failure semantics matter: if an extension initialiser fails, the platform needs a defined outcome (fail the registration, or register the tenant and surface the extension as un-provisioned) rather than an implicit one.
- A tenant must not be usable while half-initialised: base setup and every installed extension's setup must all finish before the tenant opens to ordinary users. The mechanism needs a completion barrier across base plus all installed extensions, ideally without a bespoke lock service. The base half of that barrier exists: the tenant row's `status` (`pending`, `done`, `error`) is read by every entry point (HTTP authentication, the NATS auth callout and the Temporal claim mapper), and a non-system caller is refused, the tenant's own admin included, until the row is `done`. The extension half does not exist yet.
- Extensions should be free to implement setup however they like (any service, any Temporal namespace or task queue, or no workflow at all). The base should not assume a shared queue or namespace, or even that setup is a Temporal workflow.
- The `extensions` set ([ADR-26W27-01](./adr-26w27-01-tenant-extensions-field.md)) is the authoritative statement of which extensions a tenant has; the hook must key off it, not off extension-specific fields.

## 2 Decision

Establish a **base-plus-extension architecture** in which the base owns tenant lifecycle and exposes **extension hooks**, and each extension (pyck-go first) lives entirely in its own repo and registers against those hooks. No extension-specific code remains in the base.

### 2.1 Move all pyck-go code out of the base

Relocate the pyck-go-specific logic (worker provisioning, worker config, any pyck-go branching) from `pyck-ai/pyck` into `pyck-ai/pyck-go`. The base retains only a general extension mechanism. The worker-deployment logic that the base performs today for pyck-go becomes an extension initialiser owned by pyck-go and invoked through the hook below.

### 2.2 Base emits tenant-lifecycle events

On tenant registration (and other lifecycle transitions worth hooking), the base publishes a durable event through its existing outbox-to-NATS/JetStream path. The event carries the tenant identity and its `extensions` set so subscribers can filter to the extensions that apply. This reuses the `MutationEventHook`/`entityeventsoutbox` machinery already stamping tenant writes, rather than inventing a new publish path. This event is the only trigger: it tells an extension to begin its setup (section 2.3). The base neither starts nor awaits any extension workflow; extensions run their own setup however they like and report completion back through a mutation (section 2.6).

The registration event exists, with three differences from the paragraph above. It is the outbox `create` event of the tenant row (`pyck.<tenantID>.crud.management.tenant.<tenantID>.create`), published when the resolver inserts the row at `status = pending`, before any Zitadel object exists; the transition to `done` (or `error`) arrives later as the row's `update` event. Its payload is the generic mutation event message with the tenant row as `data_after` (the `registration` secret is excluded), so it carries the caller's `data` map rather than an `extensions` set. And its first consumer is the base itself: management's trigger consumer starts the registration workflow from the `create` event. An extension keyed off `create` therefore sees a tenant that is not yet provisioned; the `update` to `done` marks the point at which the base's own setup has finished.

### 2.3 Extensions self-start from the event and report completion via a mutation

An extension consumes the tenant-lifecycle event and, when the tenant's `extensions` set contains its identifier (for example `"pyck-go"`), runs its own setup however it likes: its own Temporal workflow in its own namespace and queue, a plain service, or nothing at all. The base makes no assumption about how or where that setup runs. When the setup finishes, the extension calls back a single base mutation, `ExtensionSetupComplete(id: "pyck-go")`, to report that it is done.

```
base: tenant created with extensions ["foo","bar"]  ->  outbox row  ->  NATS/JetStream event {tenantID, extensions}
extension foo: consumes event  ->  runs its own setup (any namespace/queue, or none)  ->  ExtensionSetupComplete(id:"foo")
extension bar: consumes event  ->  ...                                                 ->  ExtensionSetupComplete(id:"bar")
base: records each completion; unlocks the tenant once every extension has reported (section 2.6)
```

This replaces pyck-go's boot-time self-start with an event-driven start, and requires nothing of the base beyond publishing the event and exposing the mutation. **Every** installed extension must call `ExtensionSetupComplete`, including one that needs no initialisation (it simply calls it immediately on the event); otherwise the tenant never unlocks. That is acceptable while pyck-go is the only extension.

### 2.4 Hooks support suppressing default behaviour

The hook is not only "notify after the base is done". For steps where an extension must change the base's default, the extension point is a **callback the base consults before running its default**, and the callback may signal "handled, skip the default". Concretely, a base default step (for example the generic worker/namespace setup, or a default data-type seed) checks whether any installed extension claims that step; if one does, the base defers to the extension's initialiser instead of running its built-in behaviour. This is what lets pyck-go's per-tenant worker provisioning move out of the base's register-tenant workflow and into a pyck-go-owned initialiser without losing the ability to run in that same slot.

### 2.5 UI templates are assigned by extension setup, not branched in the template

The remote UI bundle URL template is chosen at setup time rather than by conditionals inside the template. [ADR-26W27-01](./adr-26w27-01-tenant-extensions-field.md) introduces, as a stopgap, a single template that branches on the `extensions` set via the built-in `index` membership op; this section removes that branching.

The base ships one **default template**, used for a plain tenant with no extension. When an extension's setup lifecycle runs (section 2.3), it **overrides the template at tenant level**: it writes the tenant's `remoteWebUITemplate` (and `remoteMobileUITemplate`) to its own bundle URL template, via the same suppress-default mechanism as any other base step (section 2.4). The render path then simply reads the tenant's template (the extension's override if one was written, otherwise the base default) and does no flavour or extension branching at all.

This fits the machinery that already exists: `remoteWebUITemplate`/`remoteMobileUITemplate` are tenant.data keys written today by a privileged, system-only path (`setTenantUITemplate`, validated by `ValidateRemoteUITemplate`) and read by the workflow service at query time. An extension initialiser running with base privileges is a natural writer of that same key, so the per-tenant override drops into the render path unchanged and the `denyDataUpdateForFlavourTenantsPolicy` read-only-for-tenants guard still holds against non-system callers. When more than one installed extension wants to set the template, the resolution rule (explicit precedence, or last-writer) is part of the hook contract to be finalised (section 2.6).

### 2.6 Tenants lock by default and unlock when every extension reports complete

The base needs no polling, no awaited workflows, and no assumption about how extensions run. It tracks completion as tenant state, driven entirely by the `ExtensionSetupComplete` mutation.

**Lock by default.** A tenant with a non-empty `extensions` set is created **locked**; a tenant with no extensions is created unlocked. The lock flag is `isSetupPending` (from [ADR-26W27-01](./adr-26w27-01-tenant-extensions-field.md)'s tenant-settings schema): true while locked, cleared on unlock. This is the concrete reader the flag never had, closing the "defined but inert" gap.

**Per-extension status.** Alongside the `extensions` set, the tenant carries a server-managed `extensionStatus` map, seeded at creation with an entry per installed extension:

```jsonc
extensions:      ["foo", "bar"]
extensionStatus: { "foo": "completed", "bar": null }   // null = still pending
```

`extensionStatus` is written only by the base (the mutation resolver), never by callers, and does not need to reach Zitadel.

**Unlock in the resolver.** `ExtensionSetupComplete(id)` sets `extensionStatus[id] = "completed"`, then, when no entry remains pending, clears `isSetupPending` in the same transaction. Unlock happens exactly when the last extension reports. The mutation is idempotent (a repeat call for an already-completed id is a no-op and never double-unlocks), rejects an `id` not in the tenant's `extensions` set, and is restricted to the `system`/`admin` roles, which is consistent with the lock gate below (the extension's setup runs with such credentials).

**What the lock gates.** While a tenant is locked, authentication is denied for every user except the `admin` and `system` roles, so operators, setup tooling, and extensions can still act on the tenant while ordinary users cannot sign in until setup completes. The lock flag is exposed as a tenant attribute on `Tenant.data` (read like the `extensions` set) so the UI can show a "setup in progress" warning banner for a locked tenant.

Two shipped facts bear on the role language above. The tenant's first admin now holds the `admin` project role for their own tenant, and the operator-only surfaces (`setTenantExpiry`, `restoreTenant`, the user-projection mutations) require the system caller, so `admin` here names a tenant-scoped role, not an operator. And the `status` gate that already guards a tenant during base provisioning (section 1.4) refuses every non-system caller, admins included. A lock that admits `admin` while extensions set up would be looser than the gate the base applies to its own setup; the two need one rule before this section is implemented.

**Stuck-locked and failure.** Because the base only reacts to the mutation, an extension that never reports (crashed, undeployed, buggy, or forgot the call) leaves the tenant locked indefinitely, and there is no awaited workflow to time out. Recovering a stuck tenant therefore needs an explicit escape hatch. Remaining details to finalise: an operator force-unlock (and/or a reaper that flags or clears tenants locked past some age), whether a re-registration or extension redeploy re-emits the event to retry, and multi-extension template precedence (section 2.5). Note the trade-off: unlock is the extension's self-report, so the base trusts that report rather than observing the work itself (see the negatives).

## 3 Consequences

### 3.1 Positive

- **The base stops knowing about pyck-go.** No `if pyck-go` branch, no pyck-go worker config in base config, no extension release coupled to a base release. New extensions plug in without base changes.
- **Extension initialisation is event-driven and durable.** It reuses the outbox-to-NATS path and Temporal, so it inherits at-least-once delivery and workflow durability instead of boot-time self-start.
- **`extensions` becomes the single dispatch key.** The base fans an event out; each extension self-selects by membership. Composition (a tenant with several extensions) needs no base coordination.
- **Defaults are overridable, not just observable.** The suppress-default callback lets an extension replace a base step (worker provisioning being the concrete first case) cleanly, which a notify-only hook could not do.
- **Template choice leaves the template.** The base default UI template carries no extension conditionals; each extension assigns its own template at tenant level during setup (section 2.5), so adding an extension never means editing a shared branching template, and ADR-26W27-01's interim `index`-based branching is retired.
- **The lock is tenant state, and `isSetupPending` is finally read.** The lock is a per-extension `extensionStatus` map cleared by the `ExtensionSetupComplete` mutation, so there is no polling, no awaited workflow, and no bespoke lock service. `isSetupPending`, defined but inert today, becomes the lock flag: read by the auth gate (deny all but `admin`/`system` while locked) and surfaced on `Tenant.data` for the UI's setup-in-progress banner.
- **Extensions are fully decoupled.** The base makes no assumption about how an extension runs its setup: any service, any Temporal namespace or queue, or none. It requires only that each extension consume the NATS event and call one mutation when done, so a new extension needs no base coordination beyond that contract.
- **No base-vs-extension race.** A tenant with extensions is locked from creation and unlocks only when the last extension reports complete, so a partially-initialised tenant is never open to ordinary users regardless of the order base and extension work finish in.

### 3.2 Negative

- **A general extension point is a bigger commitment than an `if`.** The base must define and version a stable hook contract (event schema, callback interface, ordering, failure semantics). Getting that wrong is more expensive than a hard-coded branch.
- **Unlock is a self-report the base cannot verify.** The flip side of decoupling: `ExtensionSetupComplete` is the extension's word that it finished. A buggy extension that calls it too early unlocks a tenant whose setup is not actually done, and the base has no way to observe the real work (unlike an awaited workflow, whose completion it would see). The base trusts the report.
- **No automatic timeout on a stuck tenant.** Because the base only reacts to the mutation, an extension that never reports (crashed, undeployed, or forgot the call) leaves the tenant locked indefinitely with nothing to time out. An operator force-unlock, and probably a reaper for tenants locked past some age, are required, not optional (section 2.6).
- **Every extension must call the mutation, even no-op ones.** An extension with no setup work still has to call `ExtensionSetupComplete` on the event, purely to release the lock. Forgetting the call, or a typo'd id, locks the tenant. This contract is easy to get wrong and lives in each extension, not the base.
- **Distributed initialisation is harder to reason about.** Tenant setup spans services (base plus each extension), coordinated only by an event out and a mutation back. The tenant exists but stays locked (auth denied to all but `admin`/`system`) until every extension reports, so the window is explicit rather than a silently partial-usable state, but reasoning about it means tracing an event-and-callback flow across repos.
- **Suppress-default callbacks add coupling risk.** An extension point that can cancel base behaviour is powerful and easy to misuse; two extensions contending for the same step, or an extension that suppresses a step and then fails, need defined resolution.
- **Large migration.** Extracting worker provisioning and config from the base and re-homing it in pyck-go, while keeping existing pyck-go tenants working throughout, is a multi-step move, not a single cut. It should follow, not precede, the flag change in [ADR-26W27-01](./adr-26w27-01-tenant-extensions-field.md).
- **Cross-repo choreography.** The event contract lives in the base and every extension depends on it, so contract changes become multi-repo coordination. This is the intended trade (decoupling extension logic) but it moves the coupling to the wire format.
