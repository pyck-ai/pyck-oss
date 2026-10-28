# Contributing to pyck

Contributions are welcome. The org rules for issues, commits, pull requests and reviews live in [pyck-ai/guidelines](https://github.com/pyck-ai/guidelines/blob/main/AGENTS.md); this file only names what is specific to pyck.

## Lifecycle

- File or pick an issue first; it holds the intent and acceptance criteria (org [issue guidelines](https://github.com/pyck-ai/guidelines/blob/main/docs/issues.md)).
- Branch and commit per the org [git guidelines](https://github.com/pyck-ai/guidelines/blob/main/docs/git.md). pyck is stricter on one point: CI rejects a commit subject over 50 characters. Run `task lint:commits` before pushing; the full check is described in [docs/COMMIT_STANDARDS.md](./docs/COMMIT_STANDARDS.md).
- Open the pull request per the org [pull request guidelines](https://github.com/pyck-ai/guidelines/blob/main/docs/pull-requests.md).

## Before you push

```sh
task generate       # regenerate Ent, GraphQL and supergraph output after schema changes
task lint           # golangci-lint, limitlint, version groups
task test           # fast unit tests (CI also runs task test:slow on main)
task build          # all binaries
task lint:commits   # the commit-message check CI runs
```

Setup and prerequisites are in the [README](./README.md#getting-started); `task --list` shows every other command. Integration suites against a live stack live in `tests/integration/` (see its [AGENTS.md](./tests/integration/AGENTS.md)).

## Getting help

Ask in the [pyck Slack](https://join.slack.com/t/pyck-community/shared_invite/zt-3ulnckg7r-kBk6Spkeyk_DldYQpDmcxA).
