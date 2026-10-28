# ADR-26W43-04: Go code is the internal source of truth, the generated contract files are the external one

> **🟡 Status: Proposed**
>
> 2026-10-23. Records the codegen direction that the release versioning design document ("Versioning and Compatibility", a DRAFT, section 6.1 on pages 26 and 27) states as the rule. The mechanism is an end state: the design document says `contractgen`, the contract bundle and the drift guards are "part of the target design; none of them are yet implemented" (page 26). This ADR fixes the direction, not a build plan. Related: [ADR-26W43-02](./adr-26w43-02-graphql-stays-v0.md).

## 1 Context

The design document wants every semantic guarantee of the API to be a machine-readable file that is diffed in CI for compatibility, used to generate client artifacts, and used as the oracle for the test harness: "One source, three uses" (page 26). The guarantees include defaults, validation rules, error codes, enum semantics and the idempotency policy.

Today only part of this exists. `pyck` already follows one direction for the GraphQL schema: the ent schema (Go) produces the GraphQL schema, which produces the supergraph, which consumers read. CI checks that the committed supergraph is in sync (the `supergraph-in-sync` job). The rest of the contract has no generated form: the design document lists "none" for codegen contracts today (page 2), and `workflowsdk/jsonschema.go` reflects JSON Schema from Go types but there is no builder library, no per-type contract bundle and no drift guard (page 26).

A direction has to be picked before any of that is built, because it decides which file a developer edits and which file a consumer binds to.

### 1.1 Forces

- Developers already write the behaviour in Go: ent fields with defaults, error values, validators. Duplicating it in a hand-written manifest makes two places to keep equal.
- Consumers and tests need a stable, reviewable file of record that does not change by accident.
- The compatibility verdict must be derived mechanically. The design document wants "the version is derived, not chosen" (page 27).
- The direction should match the pipeline that already works for the supergraph.

## 2 Decision

### 2.1 Code is the internal source of truth, the manifest is the external one

The Go code is the internal source of truth. The generated manifest is the external source of truth, the contract of record. Tests and consumers bind to the generated files (page 26 and 27). The design document describes this as "the inverse of generated-from-manifest", and as mirroring the existing pipeline of ent schema, GraphQL schema, supergraph, consumers.

### 2.2 Generation and committing

A generator, `contractgen`, reads the hand-written Go and emits `contract/*.yaml`. The generated files are committed (page 27). Developers edit the Go source and never the generated files, which is the rule `pyck` already applies to generated code ([AGENTS.md](https://github.com/pyck-ai/pyck/blob/main/AGENTS.md#gotchas)).

### 2.3 Two CI guards

- **Drift guard.** CI regenerates the files and fails if the committed files differ.
- **Release diff.** CI diffs the generated files against the previous release and derives a MAJOR or MINOR verdict, "same as supergraph diff" (page 27). The verdict for a release is the maximum over every file in the bundle, joined with the live gates (consumer-driven contract tests and traffic replay). A human adjudicates only the explicit "residue" row (page 27).

### 2.4 The bundle

The per-release bundle holds `supergraph.graphql`, `errors.yaml`, `enums.yaml`, `defaults.yaml`, `validation/<entity>.schema.json`, `validation.opaque.yaml`, `idempotency-policy.yaml` and `i18n/errors.<lang>.yaml` (page 27). The i18n files are authored outside Go and are not part of the compatibility contract. Only the files named here are in the decision; their formats and the opaque-validator and error-catalog designs are in the design document (pages 27 to 30) and are not decided by this ADR.

How a break is classified, and what each class costs in version terms, is owned by [releases.md](https://github.com/pyck-ai/guidelines/blob/main/docs/releases.md#choose-the-semver-bump) and [deprecation.md](https://github.com/pyck-ai/guidelines/blob/main/docs/deprecation.md#tell-an-internal-break-from-an-external-one). This ADR does not restate them.

## 3 Consequences

### 3.1 Easier

- One place to change a guarantee: the Go code, next to the behaviour it describes. The manifest cannot silently disagree with the code, because the drift guard fails on it.
- The compatibility verdict is mechanical: a diff of committed files. A reviewer sees a contract change as a diff in `contract/`.
- It reuses a pattern the team already runs for the supergraph, including the CI sync check.

### 3.2 Harder

- A new generator, `contractgen`, must be built and kept working. It does not exist today (page 26).
- Go has to express things it expresses poorly today. Validation needs a JSON Schema builder library, and constraints that JSON Schema cannot express (database lookups, multi-field invariants) become named package-level Go functions with a covering test, enforced by CI (page 28). Error codes need a registered `apierr.Definition` catalog, which also does not exist (page 26, page 28).
- The generated files must be committed and reviewed, so a code change that alters the contract touches two sets of files. The drift guard turns a forgotten regeneration into a red build, as `task generate` does for `ent/gen/` today.
- The external contract is only as precise as the generator. What `contractgen` cannot reflect from Go does not appear in the contract, and so does not appear in the diff. The design document handles this with a "residue" row and a bias: if a change alters observable behaviour and no consumer can be proven unaffected, it is a MAJOR-class break (page 25).

### 3.3 Rules out

- Hand-writing the contract manifests and generating Go from them (the inverse direction).
- Editing a generated `contract/` file by hand.
- Treating a hand-written manifest and the Go code as two independent sources that CI compares.

## 4 Alternatives considered

The design document names one alternative by name and one variant for validation. It names no other option.

### 4.1 Manifest first, Go generated from it

The inverse direction, named on page 26 as "generated-from-manifest". The document rejects it by choosing the opposite and by pointing at the existing ent to GraphQL to supergraph pipeline. It states no further argument for the rejection.

### 4.2 Reflect validation from `ent.Validate(...)` annotations

Page 27 contrasts the chosen route with "reflecting from ent.Validate(...) annotations". The chosen route authors validation through a JSON Schema builder library in Go, because JSON Schema "is already in widespread use across the codebase" and per-file output makes per-type review simple. The document gives no reason against the annotation route beyond that.

### 4.3 Hand-written JSON Schema for validation

Page 27 keeps this open: "Switching the authoring side to hand-written JSONSchema later is possible; the external contract artifact is unchanged either way." It is a deferred option for the authoring side only. It would not change this ADR, because the generated and committed file stays the external contract.
