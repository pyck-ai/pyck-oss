# ADR-26W27-01: Consolidate Tenant Flavour Flags Into a Single Set-Valued `extensions` Field

> **🟡 Status: Proposed**
>
> 2026-07-01. First of two related ADRs. This one replaces the two redundant flavour flags with a single set-valued `extensions` field and simplifies tenant-metadata sync to one direction (DB to Zitadel); [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md) consumes that field as the tenant-side registry that tells the base which extension initialisers to invoke.
>
> Amended 2026-09-14. Section 2.3 (one-way sync) shipped first, as part of the DB-first tenant registration work for #1337, and it removed more than this record asked for. The Context and section 2.3 now describe the code after that change, and each deviation is stated where it applies. The field consolidation (sections 2.1, 2.2, 2.4 and 2.5) is still pending.

## 1 Context

A pyck tenant carries a small map of settings in `tenant.data` (a `map[string]any` stored on the management `Tenant` row and pushed once, at registration, to Zitadel org metadata; it is never read back, see section 2.3). Today two keys in that map encode which product flavour the tenant is, and both the shape and the naming are wrong for where the platform is going.

### 1.1 Two fields, one fact

- **`flavour`** (string): names which flavour the tenant is, for example `"pyck-go"` or `"custom-wms"`.
- **`isPyckGo`** (bool): answers the yes/no question "is this the pyck-go flavour".

Both were introduced together in the base repo (`3d92c709`, "Add tenant flavour system with metadata sync"), not in a clean boolean-then-string migration. The string is the general concept; the boolean is one special case of it. They have coexisted since, and nothing keeps them in agreement. The base repo's own tests encode the divergence: `remote_ui_test.go` asserts that `{"flavour": "custom-wms", "isPyckGo": true}` resolves to `"custom-wms"`. A tenant can therefore be simultaneously "pyck-go" (per the boolean the frontend reads) and "custom-wms" (per the string the backend reads). Two fields for one fact is a latent drift bug: any writer that sets one and forgets the other produces a tenant that behaves inconsistently across services.

The redundancy is strictly one-directional. `isPyckGo` is fully derivable as `flavour == "pyck-go"`, but `flavour` is not derivable from `isPyckGo` (the boolean cannot express `"custom-wms"`). The string carries no less information than the boolean, and more.

### 1.2 The name is wrong

"Flavour" reads as a product edition or SKU tier: one packaged variant chosen at signup. But the thing it controls is closer to feature toggles, types, or mixins: behaviours and features bolted onto a base tenant. The intended long-term architecture ([ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md)) is an explicit base-plus-extension model, where pyck-go is a pure extension on the base platform and hooks into base lifecycle events. The field that records which extensions a tenant has should be named for that role, not for a marketing edition.

### 1.3 The cardinality is wrong

`flavour` is one-of: a tenant is `"pyck-go"` xor `"custom-wms"` xor nothing. But the real world the pyck-go repo already models is many-of. pyck-go's own onboarding is three independent concerns, not one monolith:

- `workflows/010_onboarding_warehouse`
- `workflows/011_onboarding_shopify`
- `workflows/012_onboarding_sendcloud`

Warehouse, Shopify, and Sendcloud are composable capabilities, not mutually exclusive flavours. A single string cannot express "warehouse plus Shopify but not Sendcloud". Forcing composable extensions through a one-of field, and duplicating one of its values into a boolean, are the same modelling error at two rungs of the same ladder.

### 1.4 Where the keys are defined and read

In the base repo (`pyck-ai/pyck`):

- `backend/common/workflow/remoteui.go` defines the canonical `DetectFlavour(data map[string]any) string`. Precedence: a non-empty `flavour` string wins, else an `isPyckGo == true` bool maps to `"pyck-go"`, else `""`. The two keys already feed one resolved value. The UI-template render dimension `UITemplateContext.Flavour` is a single sanitized string spliced into a bundle URL.
- `backend/management/core/remote_ui.go` aliases `DetectFlavour`. It used to also hold `zitadelSyncedKeys`, `booleanFields` and `MergeData`, the machinery that read these keys back from Zitadel and overlaid them onto the DB row; that machinery is gone (section 2.3).
- `backend/management/workflows/register-tenant/workflow.go` gates worker deployment on `core.DetectFlavour(row.Data) == "pyck-go"`, where `row` is the tenant row the workflow loads: registration is DB-first, so the resolver stores the caller's `data` on a `pending` row and the workflow reads it from there, not from its input.
- `backend/management/workflows/register-tenant/datatypes/tenant-settings.json` declares `isPyckGo`, `isSetupPending`, and `flavour` (`additionalProperties: false`).
- `backend/management/workflows/register-tenant/activities.go` pushes the whole `tenant.data` map to Zitadel org metadata once, at registration (`SetOrgMetadataActivity`: keys in sorted order, one string value per key). `zitadel-sync` no longer touches tenant rows; it projects users only.

In the frontend repo (`pyck-ai/frontend`):

- `apps/pyck/src/tenant/tenant-features.store.ts` is the one true consumer of the boolean: `parseTenantFeatures` does `isPyckGo: data?.isPyckGo === true`, exposed via `useIsPyckGo()`. It does not read `flavour`.

In the pyck-go repo (`pyck-ai/pyck-go`):

- `tools/env-manager/src/services/tenant.ts` registers e2e tenants with `data: { flavour: 'pyck-go' }`; it never writes `isPyckGo`.
- CI/deploy scopes artifacts under `flavours/pyck-go/<env>/` and `scope: flavour-go`, all keyed off the string.

### 1.5 Forces

- The backend already funnels the value through one resolver (`DetectFlavour`), so backend consumers can migrate behind renamed accessors with minimal churn.
- Exactly one consumer, the frontend `parseTenantFeatures`, reads the raw boolean by name. It must change in lockstep or every tenant silently becomes non-pyck-go.
- The UI-template render dimension is currently a single `Flavour` string that templates branch on (`{{if .Flavour}}`) and that may be spliced into a URL (validated by `validateUIBundleValue`). A set does not branch or splice the way a scalar does; the render path needs the whole set exposed for membership tests, and any element spliced into a URL still inherits the allow-list. This in-template branching is an interim shape only; [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md) moves the template choice out of the template entirely.
- Zitadel org metadata stores strings, not JSON arrays or bools, so a set-valued field must be encoded when written to Zitadel. These keys used to round-trip: the `zitadel-sync` reconciliation read them back from Zitadel into the DB (`zitadelSyncedKeys` overlaid by `MergeData`, with `booleanFields`/`strconv.ParseBool` decoding). That two-way sync was extra machinery, and keeping it would have forced the set to be decoded and merged on every reconcile. It has been removed (section 2.3), so the set only ever needs encoding on the way out.
- Existing tenants carry a scalar `flavour` and/or an `isPyckGo` bool; the migration must not strand them.

## 2 Decision

Replace both keys with a **single, set-valued field named `extensions`**: a list of extension identifiers a tenant has installed. `isPyckGo` is removed as a stored field and becomes a derived membership predicate; `flavour` is renamed and widened from one-of to many-of.

```jsonc
// tenant.data
"extensions": ["pyck-go"]          // later, composed: ["pyck-go", "shopify", "sendcloud"]
```

"Extensions" is chosen over the alternatives (`plugins`, `addons`, `capabilities`, `modules`, `profile`, `edition`) because it reads as "adds to a base", is widely understood to both extend and override host behaviour (as editor and browser extensions do), and does not collide with the existing `tenant-features.store.ts` naming the way `features` would. `plugins` was the close runner-up, but the term is settled: "extension" is used consistently across both ADRs, including [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md)'s extension-hook and extension-point vocabulary. `profile`/`edition` are rejected outright because they re-encode the one-of framing this ADR removes.

### 2.1 Membership predicate replaces both equality and the boolean

- The `DetectFlavour` accessor is replaced by `TenantExtensions(data) []string` (the parsed, de-duplicated set) and `HasExtension(data, id string) bool`. Both live in the canonical home (`backend/common/workflow`) and are aliased from `backend/management/core`, exactly as `DetectFlavour` is today, so management and the workflow service cannot drift.
- `DetectFlavour(data) == "pyck-go"` becomes `HasExtension(data, "pyck-go")`; the worker-deployment gate in `register-tenant/workflow.go` becomes `if core.HasExtension(input.Data, "pyck-go")`.
- `isPyckGo` is removed from `zitadelSyncedKeys`, from `booleanFields`, and from the `tenant-settings.json` schema, which gains an `extensions` array-of-strings property in place of the `flavour` string property.
- Frontend: `parseTenantFeatures` computes `isPyckGo: (data?.extensions ?? []).includes('pyck-go')`. The `useIsPyckGo()` hook keeps its name and call sites; only its source changes. New per-extension hooks (for example `useHasExtension('shopify')`) follow the same shape.

### 2.2 UI-template render dimension (interim)

For now the render dimension becomes the **whole `extensions` set**, and templates that branched on the scalar `{{if .Flavour}}` become membership checks using a generic, default template operation, not a bespoke extensions helper. The render context exposes the set as `Extensions map[string]bool` (built from `TenantExtensions`) in place of the single `Flavour` string, so a template tests membership with the `text/template` built-in `index`:

```
{{if index .Extensions "pyck-go"}}...{{end}}
```

`index` is a stdlib built-in; no funcmap entry and no extension-specific function are added (the render path today registers no funcmap at all). Exposing the set as a map keeps the membership op generic and dependency-free; if a list shape is preferred instead, the generic list-contains is Sprig's `has` (already in the module graph), still not extension-specific. Any extension identifier spliced into a URL rather than only branched on still passes `validateUIBundleValue`'s allow-list.

This in-template branching is deliberately a stopgap: it keeps the existing single-template-with-conditionals model working under a set-valued field, with no lifecycle changes. [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md) replaces it by moving the template choice into the extension setup lifecycle, so the base default template carries no extension conditionals and each extension overrides the template at tenant level. Once that lands, the `index`-based branching added here is expected to be removed.

### 2.3 Sync becomes one-way (DB to Zitadel)

The flag keys used to round-trip: management wrote them to Zitadel org metadata, and the `zitadel-sync` reconciliation read them back into the DB, overlaying `zitadelSyncedKeys` via `MergeData` (with `booleanFields` decoding Zitadel's strings). The read-back is dropped. The management DB (`tenant.data`) is the single source of truth, and sync runs one direction only: **DB to Zitadel**. Management pushes the tenant-data keys (and `extensions`, once it exists) out to Zitadel org metadata; Zitadel edits never flow back into the DB.

This shipped with the DB-first tenant registration work, and it went further than the paragraph above. `zitadelSyncedKeys`, `MergeData`, `booleanFields` and `IsBooleanField` are gone, and `zitadel-sync` no longer reads tenants from Zitadel at all: the orchestrator lists live, fully registered tenants from the DB and projects each org's users, nothing else. The org-name mirror, the soft-delete of tenants whose org had vanished and the unauthenticated `/webhook/zitadel/sync` endpoint were removed together with the read-back, so a tenant whose org is renamed, edited or deleted out of band in the Zitadel console keeps its row as it is until an operator runs `deleteTenant`. The push is one write per registration (`SetOrgMetadataActivity`); nothing in this repo reads org metadata back (the workflow service's render dimension reads `tenant.data` from management over GraphQL), so the push serves external consumers only. The stored `isPyckGo` bool is not removed by this step; it goes with the field consolidation in section 2.1.

### 2.4 Storage encoding

- In the management DB (`tenant.data` JSON), `extensions` is a native JSON array of strings. This is the source of truth. Note that `registerTenant`, as built, refuses nested or array values in `data` (`validateTenantData`), precisely because the push writes each key to org metadata as one string; storing an array under `extensions` through that mutation needs the check and the push to treat this key specially.
- In Zitadel org metadata (string values only), the set is encoded as a stable canonical string (sorted, comma-separated, or JSON-encoded) under the `extensions` key when management pushes DB to Zitadel. There is no decode-back: sync is one-way, so Zitadel never writes to the DB and the encoded form is write-only from management's side.

### 2.5 Migration and rollout

- Map scalar `flavour: "x"` to `extensions: ["x"]`; map an `isPyckGo: true` with no non-empty `flavour` to `extensions: ["pyck-go"]`; an absent or empty flavour maps to an absent `extensions` (or `[]`).
- Write the backfill to the management `tenant.data` column (the source of truth); the one-way DB-to-Zitadel push carries it out to Zitadel org metadata. With the read-back removed, there is no reconcile step to undo a DB write, so no separate Zitadel write is needed beyond the normal push.
- Ship the backend read path tolerant of the old keys during rollout (prefer `extensions`, fall back to a scalar `flavour`, then to the `isPyckGo` bool), then remove the fallbacks once the backfill is verified and the frontend reads `extensions`.

## 3 Consequences

### 3.1 Positive

- **One fact, one field.** The drift hazard (a tenant that is pyck-go to the frontend and custom-wms to the backend) is structurally impossible once the boolean is gone and a single field remains.
- **The field name matches its job.** `extensions` describes composable extensions bolted onto a base tenant, which is what the value controls and where [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md) is heading.
- **Composition becomes expressible.** A tenant can hold warehouse, Shopify, and Sendcloud independently instead of being pinned to one flavour, matching the extension structure the pyck-go repo already has.
- **The extension registry is now first-class.** [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md)'s base hooks can enumerate `extensions` to decide which extension initialisers to run, with no bespoke per-flavour branching in the base.
- **Backend churn stays small.** Backend consumers already resolve through one function; it becomes two helpers, equality checks become membership checks, and the render path exposes the set for a built-in `index` membership test.
- **Sync is one direction (shipped).** Dropping the Zitadel-to-DB read-back makes the DB the unambiguous source of truth and deletes the `zitadelSyncedKeys`/`MergeData`/`booleanFields` reconcile-and-decode machinery. A tenant's identity has one editable home, and there is no reconcile drift between what Zitadel says and what the DB says.
- **Smaller schema surface.** One key instead of two, the synced-key set and boolean-conversion list removed outright, and one fewer thing every tenant writer must remember to set.

### 3.2 Negative

- **Cross-repo, multi-step change.** The change spans the base repo (schema, sync, resolver, render dimension) and the frontend repo (`parseTenantFeatures`), and it both drops a key and changes another's shape. The steps must land in a compatible order: backfill the DB first (the one-way push carries it to Zitadel), then switch the frontend to `extensions`, then remove the old-key fallbacks. A reader left comparing a scalar (or reading the boolean) against the new array silently mis-evaluates.
- **One-way sync removes the Zitadel-edit back-channel (shipped).** Any flow that changed a tenant flag by editing Zitadel org metadata directly (relying on reconciliation to pull it into the DB) has stopped working; such changes must go through management (the DB). No management mutation for tenant flags was added with the removal, so until one exists the remedies for an out-of-band Zitadel change are `deleteTenant` and a fresh registration.
- **Zitadel still needs a set encoding.** Metadata is string-valued, so the set is serialised on the DB-to-Zitadel push. The encoding should be canonical (stable order) so repeated pushes of an unchanged set do not rewrite Zitadel metadata needlessly. There is no parse-back to get wrong, since sync is one-way.
- **URL templates change shape twice.** In this ADR any template using `{{.Flavour}}` moves to a built-in `index` membership check over the exposed set; a missed template renders an empty segment. This is interim: [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md) then removes the in-template branching altogether in favour of a lifecycle-assigned template, so template authors churn once now and once more later.
- **Irreversible in practice.** Once the old keys are dropped and callers read `extensions`, re-adding `isPyckGo` or a scalar `flavour` would recreate the drift this ADR removes. A reversal would be a new ADR, not a revert.
