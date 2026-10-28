# ADR-26W27-03: Persist Frontend State in the KV Store via a Generic `frontend-state` Datatype

> **🟡 Status: Proposed**
>
> 2026-07-01. Independent of, but adjacent to, [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md): that ADR governs authoritative backend setup state (the tenant lock `isSetupPending` and per-extension `extensionStatus`); this ADR gives the frontend a generic place to persist its own view state, which is deliberately **not** authoritative. It uses the existing KV store and datatype mechanism and has no dependency on [ADR-26W27-01](./adr-26w27-01-tenant-extensions-field.md)'s `extensions`.

## 1 Context

The frontend persists small pieces of its own state per tenant, and sometimes per user: "has component X been initialised", "should banner X still show", "has the user acknowledged Y", locale preference, and so on. This is client-owned view state, distinct from the backend's authoritative tenant state.

Today this is scattered two ways, both ad hoc:

- **Browser-local storage.** Several Zustand stores and helpers persist to `localStorage`, each with its own key scheme: `common/dismissed-banners`, `common/i18n` (locale), `onboarding/onboarding-dismiss` (`pyck:onboarding-dismissed:<userId>:<tenantId>`), `debug/debug-settings`, `device/device-store`, `auth/utils/session-expiry`, and `packages/data-table`. This state is **per browser and per device**: it does not follow a user to another machine, is lost on a cache clear, and is invisible to the server. Each microfrontend (module federation) that wants it pokes `localStorage` directly.
- **KV store, pyck-go-shaped.** The one piece of frontend state that is server-persisted, onboarding completion, goes through the management KV store: `OnboardingGate` reads `keyValues(where: { name: "onboarding", userIDNotNil: true })`, checks `node.data.data.status === 'completed'`, gated on `useIsPyckGo()`. It stores under the generic `keyvalue` datatype envelope (`{ meta: {type, version}, data: {…} }`) with a bespoke `data.status` convention.

So durable frontend state has no single, generic, cross-device home. `localStorage` is per-browser and scattered; the one server-backed case is pyck-go-gated and uses a one-off convention.

The KV store and the machinery to validate a value against a datatype already exist. `keyvalue` (an open envelope) and `tenant-settings` are base default datatypes installed for every tenant by `AddDefaultDataTypesActivity` (`backend/management/workflows/register-tenant/datatypes/*.json`, `default: true`). KV entries are keyed by a `name`, optionally scoped to a `userID`, tenant-isolated, and policy-gated (`keyvalue_policy.go`). What is missing is (a) a generic, base-owned datatype for frontend view state, and (b) a decision to route durable frontend state to it rather than to the browser.

### 1.1 Forces

- Frontend view state is open-ended and evolves with the UI; adding a flag should not require a backend schema change.
- Durable per-user or per-tenant state should follow the user across browsers and devices and survive a cache clear; `localStorage`/`IndexedDB` cannot do this.
- It must be base-owned and generic: usable by base frontend features and by any extension's frontend, with no `isPyckGo`/pyck-go gating and no extension having to install it.
- Keys must not collide across features or extensions, so they need a namespace.
- It is not authoritative: it must never be confused with, or substituted for, backend state that drives access or correctness (the tenant lock and `extensionStatus` in [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md)).
- Not all client state should move: some is genuinely device-specific (device store), some bootstraps auth before a KV call is even possible (session expiry), and some is hot/ephemeral where a network round-trip per change is unacceptable. The rule must distinguish durable-cross-device state from device-local/ephemeral state.
- It should reuse the existing KV store, datatype validation, and access policies rather than adding a new store.

## 2 Decision

Two parts: a generic datatype, and a policy to route durable frontend state to the KV store under a namespaced key.

### 2.1 A generic `frontend-state` datatype

Add a base default datatype **`frontend-state`**, a plain open object stored in the KV store, that the frontend may fill with anything.

```json
{
  "name": "Frontend State",
  "slug": "frontend-state",
  "description": "Generic bag for frontend-owned UI state (component-initialised flags, banner visibility, acknowledgements, preferences). Client-owned and non-authoritative.",
  "entity": "keyvalue",
  "default": true,
  "jsonSchema": {
    "$schema": "http://json-schema.org/draft-07/schema#",
    "type": "object",
    "additionalProperties": true
  }
}
```

It lives next to the existing defaults (`register-tenant/datatypes/frontend-state.json`, `default: true`) and is installed for every tenant by the base's `AddDefaultDataTypesActivity`, exactly like `keyvalue.json` and `tenant-settings.json`. No extension installs it. Intentionally simpler than the `keyvalue` datatype: a bare open object, with no required `meta`/`data` envelope. The frontend owns the structure.

### 2.2 Durable frontend state lives in the KV store under a `pyck.ai/frontend/` namespace

The frontend persists its durable state as `frontend-state` KV entries instead of in `localStorage`/`IndexedDB`. Each entry's KV `name` is a **fully-qualified, namespaced key** under a reserved `pyck.ai/frontend/` prefix, and its value is any JSON:

```
pyck.ai/frontend/onboarding.status              = { "status": "completed" }
pyck.ai/frontend/banner.releaseNotes.dismissed  = true
pyck.ai/frontend/component.stockTable.initialised = true
pyck.ai/frontend/pref.locale                    = "de-DE"
```

- **One entry per key**, not one large bag. Using the KV `name` as the key gives `localStorage`-like get/set semantics over the existing `keyValues`/`setKeyValue` API, and avoids the read-modify-write races a single shared object would create.
- **Scope** follows the KV store: per-user state sets `userID` (so it does not leak across accounts on a shared machine, matching today's `<userId>:<tenantId>` local key); per-tenant shared state omits it. Tenant isolation and access are the existing KV policies; no new authorization.
- **The namespace** (`pyck.ai/frontend/…`) separates frontend state from other KV uses and gives each feature or extension a collision-free sub-namespace (for example `pyck.ai/frontend/ext.pyck-go.…`).

### 2.3 What stays local

"All frontend state" means all **durable, cross-device, post-login** state. Explicitly kept in browser storage:

- **Device-specific state** (the device store): it describes the machine, not the user, so cross-device sync would be wrong.
- **Auth bootstrap** (session expiry): it gates login and must be readable before an authenticated KV call is possible.
- **Hot or ephemeral UI state** where a per-change network round-trip is unacceptable (transient in-progress interactions); these stay in memory or `localStorage`.

Everything currently in `localStorage` that is durable and user-scoped, dismissed banners, locale, onboarding status and dismiss, migrates to `frontend-state` keys. The onboarding-complete flag moves from the ad-hoc `keyvalue` "onboarding" entry to `pyck.ai/frontend/onboarding.status`, and `OnboardingGate` reads it there; the hard `useIsPyckGo()` gate is no longer needed for the storage to exist.

### 2.4 Access pattern

Reads go through the existing `keyValues` query (Relay-cached, so repeat reads are not re-fetched); writes through `setKeyValue`, debounced and optimistically applied for chatty updates so the UI stays responsive despite the round-trip. A thin store wrapper can present a `localStorage`-like `get(key)`/`set(key, value)` surface over these so feature code does not deal with GraphQL directly.

## 3 Consequences

### 3.1 Positive

- **State follows the user.** Durable per-user state persists across browsers, devices, and cache clears, which per-browser `localStorage`/`IndexedDB` never did. Dismissed banners, locale, and onboarding status stop resetting when the user switches machines or clears storage.
- **One generic, governed home.** All durable frontend state lives in one tenant-isolated, policy-gated, auditable store instead of scattered ad-hoc `localStorage` keys, and it is uniform across the federated microfrontends rather than each remote poking the browser directly.
- **No backend change per new flag, no collisions.** The open object lets the frontend add keys freely; the `pyck.ai/frontend/` namespace keeps base and extension keys from clashing.
- **Base-owned, so extensions inherit it.** Because `frontend-state` is a base default on every tenant, pyck-go and any future extension already have it, closing the "pyck-go installs the frontend's state store" coupling and the `isPyckGo` gate.

### 3.2 Negative

- **Network round-trip replaces synchronous storage.** `localStorage` reads and writes are instant and offline; KV reads and writes are async and need connectivity. This forces caching, optimistic updates, and debouncing, and it rules out the store for offline-required or latency-critical state. "All state" is therefore really "all durable, online, post-login state", and mis-classifying a value (moving device/ephemeral state, or leaving durable state local) has a cost either way.
- **Requires an authenticated session.** Nothing can be read or written before login, and, per [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md), while a tenant is locked only `admin`/`system` can authenticate, so ordinary users' `frontend-state` is unreachable until unlock (acceptable, since they cannot use the app either).
- **Write volume and no cross-key transactionality.** Chatty UI state can generate many KV writes (needs batching/debounce), and each namespaced key is its own entry with no atomic multi-key update.
- **No schema enforcement, and two open datatypes.** `frontend-state` accepts anything, so a malformed value is stored silently; and it overlaps conceptually with the `keyvalue` envelope (a flat FE-owned bag versus a `{meta,data}` envelope), a distinction that is convention, not enforced. A later ADR could subsume one into the other.
- **Client-writable and non-authoritative.** Because the frontend writes it, it must never hold state that gates access or correctness; that belongs to backend state ([ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md)'s lock and `extensionStatus`). Its convenience invites misuse as a source of truth; this ADR calls that out, but nothing structurally prevents it.
- **Migration effort.** Several existing `localStorage` stores must be moved key by key, with a decision per store on whether it is durable-cross-device (move) or device/ephemeral/bootstrap (keep), plus a one-time import so users do not lose current local state.
