# ADR-26W43-03: Roll back by redeploying the previous release manifest, not by down migrations

> **🟡 Status: Proposed**
>
> 2026-10-23. Records a decision that the release versioning design document ("Versioning and Compatibility", a DRAFT, section 3.8 on page 23) states as made. The repository owner adopted it on 2026-10-23 as the rule for the org guidelines. The design document calls the release manifest the BOM; this ADR uses "release manifest", as [releases.md](https://github.com/pyck-ai/guidelines/blob/main/docs/releases.md#terms) does. Related: [ADR-26W43-01](./adr-26w43-01-single-deployment-of-the-saas.md).

## 1 Context

Each service owns its schema in `<svc>/ent/schema/` and generates migrations with Atlas over a shadow database into `<svc>/ent/migrate/migrations/`, in golang-migrate format with an `atlas.sum` hash that CI verifies (page 23, `task atlas:migrate`). Down files exist next to the up files: `pyck` has 154 `*.down.sql` files under `backend/*/ent/migrate/migrations/` today.

A release can go wrong after it is deployed. There are two ways to undo it:

- Run the down migrations of the new release, then deploy the old code.
- Redeploy the previous release as a whole: the artifacts and the toggle state of the previous release manifest.

The design document chooses the second (page 23): "Rollback is via the previous BOM, not down-migrations. Down-migrations exist in the migration directories but are for local-dev cleanup, not production rollback."

### 1.1 Forces

- A down migration is code that runs against production data at the worst moment, under incident pressure, and a down step cannot restore data that a destructive step deleted.
- The release manifest already pins the artifacts and the toggle state together (page 24), so one redeploy undoes both. The design document says every toggle flip is "undone by rolling back to the previous BOM".
- Old and new code run against one schema during a rollout, so a schema change must be safe for both (page 23, expand and contract).
- [ADR-26W43-01](./adr-26w43-01-single-deployment-of-the-saas.md) fixes one deployment, so rollback has exactly one target.

## 2 Decision

### 2.1 Production rollback redeploys the previous release manifest

Production rollback redeploys the previous release manifest in `pyck-ai/deployment`. That restores the artifacts and the toggle state of the previous release together. Down migrations are not part of production rollback ([releases.md](https://github.com/pyck-ai/guidelines/blob/main/docs/releases.md#deploy-and-roll-back)).

### 2.2 Down migrations are optional and for local development

Down migrations stay in the migration directories, for local-dev cleanup only (page 23). Writing one is optional.

### 2.3 A migration must let the previous release run

The previous release's code must run against the schema the new migration produces. The schema either runs identically (no migration since) or runs with the additive parts of the new state present, which is forward-compatible under expand and contract (page 23). A migration that fails this test is treated as breaking and follows the bump-tier rules (page 23).

### 2.4 Expand and contract is the default shape

Expand and contract is the default migration pattern. A multi-step rename or restructure spans at least one Platform Release boundary: add the new, dual read and write, backfill, then drop the old. No single migration does both an add and a drop, so a rollback to the previous release manifest "remains available at every step" (page 23). The steps, and the merge gate "the previous release's code runs against the new schema", are owned by [changing-code.md](https://github.com/pyck-ai/guidelines/blob/main/docs/changing-code.md#change-a-schema-in-additive-phases). This ADR does not restate them.

## 3 Consequences

### 3.1 Easier

- One rollback procedure for code, configuration and toggles. It does not depend on the content of any migration.
- No production code path runs a down migration, so there is no untested down step to trust during an incident.
- Review has one question for a migration: does the previous release still run on it.

### 3.2 Harder

- Every destructive schema change is split across releases. A drop or a rename waits for a later Platform Release and gets its own deploy. Customer-read shapes wait for the next Platform Release (this timing rule is in changing-code.md; the design document says "at least one Platform Release boundary", page 23).
- Contributors need to learn the multi-phase pattern for renames and prove the previous release against the new schema before merge. The design document states the rule but names no tooling for the proof. The `check-drift` job in `pyck` CI compares the ent schema with the migrations; it does not run the previous release's code against the new schema.
- Rollback restores artifacts and toggles but does not undo data the new release wrote. This follows from the page 23 test (the previous release must run on the migrated schema), and the design document does not spell it out.
- A release that needs an irreversible step cannot ship it as a normal migration. It becomes a breaking change under the bump-tier rules and, where it changes behaviour, a flag day behind a toggle ([ADR-26W43-01](./adr-26w43-01-single-deployment-of-the-saas.md)).

### 3.3 Rules out

- Running down migrations in staging or production as a rollback step.
- Treating "the down migration exists" as proof that a migration is safe to merge.
- A single migration that both adds the new shape and drops the old one.

## 4 Alternatives considered

The design document names down migrations only to set them aside for production (page 23). It does not weigh further options. Section 4.2 is implied by the text.

### 4.1 Run down migrations to roll back

The conventional alternative. Not adopted: down migrations are for local-dev cleanup (page 23). A down step cannot restore data that a destructive step deleted, and it runs against production data under incident pressure (section 1.1). Redeploying the previous manifest also restores toggle state (page 24), which down migrations do not.

### 4.2 Roll forward only

Implied by the page 23 rule that a migration which cannot be undone by "deploy previous BOM" is treated as breaking: a fix would then go out as a new release. The document does not state roll forward as a policy, and does not forbid fixing forward when a rollback is not wanted. Redeploying the previous manifest is the documented default.
