# Architecture Decision Records (ADRs)

An ADR captures one significant architectural decision: its context, the decision, and its consequences. ADRs are immutable history; a later decision that reverses an earlier one is a new ADR that supersedes it, not an edit.

## ADRs

| ID | Title | Status |
| -- | ----- | ------ |
| [ADR-26W27-01](./adr-26w27-01-tenant-extensions-field.md) | Consolidate the redundant `flavour` string and `isPyckGo` boolean into a single set-valued `extensions` field, removing the two-fields-one-fact drift hazard and modelling composable tenant extensions instead of one mutually-exclusive product flavour. | 🟡 Proposed |
| [ADR-26W27-02](./adr-26w27-02-pyck-go-as-base-extension.md) | Move all pyck-go code out of `pyck-ai/pyck` into `pyck-ai/pyck-go` as a pure extension, driven by base lifecycle hooks (tenant-registration outbox events, Temporal signals) with callbacks that can suppress default behaviour. | 🟡 Proposed |
| [ADR-26W27-03](./adr-26w27-03-frontend-state-datatype.md) | Persist the frontend's durable UI state server-side in the KV store under a generic base `frontend-state` datatype, keyed by a `pyck.ai/frontend/` namespace, instead of scattered per-browser localStorage/IndexedDB. | 🟡 Proposed |
| [ADR-26W43-01](./adr-26w43-01-single-deployment-of-the-saas.md) | Run exactly one deployment of the SaaS at a time: no blue-green by major version and no parallel deployments, so multi-version compatibility comes from API versioning inside that one deployment and a break that cannot be versioned away is a pre-announced flag day. | 🟡 Proposed |
| [ADR-26W43-02](./adr-26w43-02-graphql-stays-v0.md) | Keep GraphQL at `v0` for good and never build parallel-versioned GraphQL schemas; consumers that need hard versioning use the versioned gRPC surface. | 🟡 Proposed |
| [ADR-26W43-03](./adr-26w43-03-rollback-by-previous-release-manifest.md) | Roll back production by redeploying the previous release manifest, not by running down migrations; a migration is safe to merge only if the previous release still runs on the migrated schema. | 🟡 Proposed |
| [ADR-26W43-04](./adr-26w43-04-code-is-the-contract-source-of-truth.md) | Treat the hand-written Go code as the internal source of truth and the generated, committed `contract/*.yaml` files as the external one, guarded in CI by a regenerate-and-compare drift check and a diff against the previous release. | 🟡 Proposed |

## Conventions

One decision per file. Each ADR opens with its status as a blockquote directly below the title — there is no `## Status` section, and no separate draft banner. The status line carries the same marker shown in the table above; the body carries the date and whatever the status needs to say:

```markdown
> **🟡 Status: Proposed**
>
> 2026-07-01. Directional ADR: it fixes the target architecture and the mechanism.
```

| Marker | Status |
| -- | -- |
| 🟢 | Accepted |
| 🟡 | Proposed |
| 🔴 | Rejected |
| ⚪ | Deprecated |

An ADR that a later decision reverses or replaces becomes ⚪ Deprecated and names the ADR replacing it — reversals add a new ADR rather than editing the body. ADRs cross-reference each other by ID.

This is a plain blockquote, deliberately not a GFM alert, so it renders the same everywhere. github.com only recognises an alert when the `[!TYPE]` marker sits alone on its line and silently drops the callout otherwise, which rules out carrying the status as an alert title. The emoji supplies the colour an alert type would have, and the one Markdown source reads correctly both on GitHub and in the PDF.

Each ADR has one ID in two cases: a lowercase filename and an uppercase reference.

```
Filename:   adr-{yy}w{ww}-{nn}-{slug}.md      e.g. adr-26w27-01-tenant-extensions-field.md
Reference:  ADR-{YY}W{WW}-{NN}                e.g. ADR-26W27-02
```

`yy` is the two-digit year, `ww` the ISO 8601 week number, and `nn` a zero-padded counter that increments per `(year, week)` starting at `01`. The `w` marks the number as a week, not a month, so the ID never reads as a date. To add an ADR, take the current year and week, pick the next free counter, and add a row to the table above.

## Section numbering

Headings inside an ADR carry a hierarchical number — `## 1 Context`, `### 3.4 Hooks support suppressing default behaviour` — so a passage can be cited as "3.4" from a printed copy. The title (`#`) is not numbered; `##` becomes `1` and `###` becomes `1.1`. The status admonition sits above the first numbered heading and is not itself a section.

The numbers are stored in the Markdown rather than generated when the PDF is rendered, so GitHub and the PDF always agree and a number can be quoted in review.

| Command | Effect |
| ------- | ------ |
| `task number` | Renumber every ADR in place. Idempotent, so it is also the renumber-after-a-change operation. |
| `task number:check` | Show what would change, without writing. |

Run `task number` after adding, removing or reordering a section. Prose that cites a section number is ordinary text and is **not** updated automatically — fix those references by hand in the same change.

## Rendering

`task pdf` renders the ADRs to PDF with [gfm2pdf](https://github.com/pyck-ai/gfm2pdf). It needs `GOPRIVATE=github.com/pyck-ai/*` (gfm2pdf is a private module) and a Chrome or Chromium binary on `PATH` or named by `CHROME_BIN`. The generated PDFs are gitignored; the Markdown is the source of truth.
