# Commit message check

pyck's commit messages follow the org [git guidelines](https://github.com/pyck-ai/guidelines/blob/main/docs/git.md). This file documents only the local CI check, [`scripts/check-commit-messages.sh`](../scripts/check-commit-messages.sh), and where it is stricter than the org rules.

## Deltas from the org guideline

- The subject line is at most **50 characters** (org: aim for 50, at most 72). CI fails the commit (rule R2).
- Everything else the script checks is the org rule made mechanical: imperative, capitalised, no period, no prefix, blank line after the subject, body wrapped at 72.

## Enforcement

CI lints every commit message on non-default, non-bot branches (the `commit-message-standards` job in [`ci.yml`](../.github/workflows/ci.yml)). It checks the range from `git merge-base origin/main HEAD` to `HEAD`, so it only ever lints your unpushed or unmerged commits.

Run it locally before pushing:

```sh
task lint:commits
```

It is a standalone task, deliberately not part of `task lint`: `task lint` runs constantly during development and must not fail on unpushed WIP commits.

### Rules

| Rule | Check | Severity |
|------|-------|----------|
| R1  | Subject line is non-empty | FAIL |
| R2  | Subject is 50 chars or fewer (after stripping a trailing `(#123)` PR-merge suffix) | FAIL |
| R4  | Subject starts with a capital letter | FAIL |
| R5  | Subject has no trailing period (an ellipsis, `...`, is allowed) | FAIL |
| R6  | Subject has no conventional-commit prefix (`feat:`, `fix(scope):`, ...) | FAIL |
| R7  | Subject is in the imperative mood (not past tense/3rd-person/gerund) | FAIL |
| R8  | Not a stray `fixup!`/`squash!` commit (autosquash before merging) | FAIL |
| R9  | Line 2 is blank (subject/body separator) | FAIL |
| R10 | Body lines are flush left (except bullet-list continuations) | FAIL |
| R11 | Body lines are wrapped at 72 chars or fewer | FAIL |
| R12 | No trailing blank lines at the end of the message | WARN |
| R14 | An issue reference uses exactly one of `Closes: #123`, `Part-of: #123` or `See-also: #123`, one issue per line (never `Closes: #1, #2`); an issue in another repo is `owner/repo#123`. A line that looks like a trailer but deviates (`Closes #1`, `Closes:#1`, `Closes: #1, #2`) fails, and so do the legacy keywords `Fixes`, `Fix`, `Resolves`, `Resolve`, `Implements`, `Refs`, `Ref`, `Related`, `See also` and `See` in trailer shape (`Fixes: #1`, `Refs #2`). Keywords are case-sensitive. Prose that merely starts with such a word (`Fixes the race in X`, `See also the ADR`) is not affected | FAIL |

`--strict` promotes WARN rules to FAIL; CI does not use it.

### Exemptions

Whole commits are skipped for:

- Merge commits.
- Bot authors (`*[bot]@users.noreply.github.com` or an author name ending in `[bot]`).
- Git-generated reverts (subject starts with `Revert "`; a hand-written
  `Revert foo` without quotes is still linted).

Rule-scoped exemption (R2 only, regardless of author): subjects matching
`Update (module|... (Docker tag|action|monorepo|image)) ...`, i.e. a
machine-shaped Renovate dependency bump. These commits still have to pass
R4-R8.

R11 additionally exempts, on a per-line basis: unwrappable single tokens
(long URLs/identifiers), trailer lines (`Key: value`, e.g.
`Co-authored-by:`, but not `BREAKING CHANGE:` which has a space in the
key), fenced code blocks (` ``` ` or `~~~`), indented code blocks (4+
spaces), and table rows (`| ... |`).
