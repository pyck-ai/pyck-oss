# tests/integration — guide for adding tests

This module holds **end-to-end integration suites** that run against a *live*
pyck stack (gateway, zitadel, temporal, nats, postgres) — not unit tests. If
you only need a DB or a single resolver, write a unit test in the owning
`backend/<service>` module instead (those use testcontainers and run on every
push). Reach for this module only when a test must exercise the federated
gateway, real Zitadel auth, or a running Temporal namespace.

## How it runs

- Every test file is behind `//go:build integration`, so a plain
  `go test ./...` is a no-op here — the suites compile and run only with
  `-tags=integration`.
- `task test:integration` (from repo root) or `task test:integration:run` /
  `./scripts/start.sh` (from this dir) sources `config/keys/bootstrap.env`,
  checks the stack is reachable, and runs every suite with gotestsum. Requires
  a **test-cadence stack**: `task up:integration` first (or
  `task test:integration:up`, which boots it and then runs). Plain `task up`
  keeps production cadences (reconcile 5m, expiry check 1m, Zitadel sync 10m,
  PAT cache 1h), which make the sweep and token-cache suites skip or time out.
  `task up:integration` layers `config/compose/integration.yaml` (5s reconcile,
  10s expiry check, 10s Zitadel sync, 5s PAT cache) over the same project, so
  `docker compose down -v` from the repo root tears it down as usual.
  **`task test` (unit tests) never runs these.**
- `scripts/envrc.sh` reads the `PYCK_*_INTERVAL` values from the running
  `pyck-management` container's environment (the stack's real cadence) so
  sweep-timing tests size their waits to it, falling back to `.env` if docker or
  the container is unavailable.
- For a single suite from your shell or IDE, load the env first — the suites
  fail at `config.Load` without it:
  `source ./scripts/envrc.sh && go test -tags=integration -run TestExpiry ./tests/tenant-lifecycle/...`
- CI: not wired yet (a follow-up). When added it should run on its own —
  separate from the `backend/*` unit-test matrix, since it needs a live stack.

## Layout (keep this shape)

```
internal/            shared, reusable, NOT build-tagged
  config/            env → Config (add fields here, default sanely)
  fixtures/          gofakeit-backed random inputs (NewTenant, NewMachineUser)
  gateway/           typed API-client + helper wrappers (RegisterTenant, …)
  temporal/          Temporal dials (API-key auth, TLSDisabled for local TCP)
  workerproc/        build + run helper worker binaries as OS processes (Start, Stop on SIGTERM)
  zitadelclient/     Zitadel SDK helpers (EnsureMachineUser, AddPAT, grants)
tests/
  <suite-folder>/    one folder per feature area; files //go:build integration
    suite_test.go    package doc + the `func TestXxx(t)` runner
    <prefix>_test.go  the suite struct + lifecycle test (prefix: tn_, wf_, …)
    helpers_test.go   suite-private helpers (optional)
```

## Rules for a new suite

1. **One folder per feature**, files prefixed with a short tag matching the
   suite (`tn_`, `wf_`, `mu_`, `tc_`, `ie_`, `ed_`). Every `*_test.go` starts with
   `//go:build integration`.
2. **Embed `tests.Base`** in your suite struct and use `testify/suite`. `Base`
   gives you `s.Ctx`, `s.Cfg`, and a dialed `s.ZConn` for free via
   `SetupSuite` — do not re-load config or re-dial Zitadel.
3. **Provision a fresh tenant per run** — never touch a shared/default tenant.
   The standard recipe (registers the tenant, creates a machine user, grants
   roles, mints a PAT, and waits out Zitadel's async projections):
   ```go
   rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
   r.NoError(err)
   s.DeferTenantCleanup(rt.ID) // IMMEDIATELY after registration, see below
   p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
   r.NoError(err) // p.PAT is ready to use
   ```
   Pass `RolesWithServiceGates("writer")` when the suite drives gated services
   through the gateway; bare `[]string{"writer"}` otherwise. `fixtures.New*`
   names never collide across re-runs or parallel packages. Call
   `s.DeferTenantCleanup` (drained in `TearDownTest`) right after
   registration — NOT as a final `s.Run("cleanup")` stage (every early
   return before it skips it, leaking a live tenant + org per failed run),
   and NOT via `s.T().Cleanup` inside a stage (inside `s.Run`, `s.T()` is
   the stage's own T, so the cleanup fires when the stage ends and disables
   the tenant mid-test).
4. **Reuse `internal/`; extend it, don't fork it.** Need another service's
   client, a Temporal connection, or a new env value? Add it to
   `internal/gateway` / `internal/temporal` / `internal/config` so the next
   suite gets it too. Suite-only helpers stay in the suite's
   `helpers_test.go`.
5. **Poll, never sleep, for eventual consistency.** Zitadel grants, Temporal
   visibility indexes, and NATS projections all lag. Build "wait until X"
   on `tests.PollUntil` / `tests.PollUntilElapsed` and "X must hold for the
   whole window" quiescence phases on `tests.PollStable` (all bounded,
   ctx-aware; PollStable fails fast the moment the invariant breaks). A
   fixed `time.Sleep` is flaky and forbidden. Size sweep-driven windows from
   the config cadences (`s.Cfg.TenantExpiryCheckInterval`,
   `s.Cfg.TenantReconcileInterval` — envrc.sh forwards the local `.env`
   overrides), and `t.Skipf` with a clear message when the stack's cadence is
   too slow for the window to prove anything (see tn_reconcile / deleted-org).
6. **Verify from two vantage points** when asserting on a propagated value
   (e.g. gateway query *and* a direct Temporal/SA read) so a half-written
   change can't pass.
7. **Don't gate on features that aren't on `main`.** These suites run against
   whatever the stack serves; if the API/mutation lives on an unmerged branch,
   the suite won't compile against `main`. Keep such code out until it lands.

## Wiring a brand-new suite into CI

Nothing to do — `scripts/start.sh` runs `./tests/...`, so a new folder is
picked up automatically. Just confirm it passes locally with
`task test:integration -- -run TestYourSuite` against `task up:integration`.
