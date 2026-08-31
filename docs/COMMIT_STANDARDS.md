# Commit and Branch Standards

This document describes our Git workflow standards and how Claude helps enforce them.

## Branch Naming Convention

**Format:** `[issue-id]-[short-description]`

**Examples:**
- `123-implement-dark-mode`
- `456-fix-login-mobile-responsive`
- `789-refactor-api-client-async`

## Commit Message Format

We follow [Tim Pope's commit message guidelines](https://tbaggery.com/2008/04/19/a-note-about-git-commit-messages.html):

```
Subject line (50 chars max, imperative, starts with capital, no period)

Blank line

Body of commit message (wrapped at ~72 chars, explaining WHY and HOW)

Blank line

Footer (e.g., references to issues: Fixes: #123, See also: #456)
```

> **50 chars is a hard limit.** CI rejects any subject line over 50
> characters (see [Enforcement](#enforcement) below) -- this isn't a
> soft guideline.

### Examples

**Simple Bug Fix:**
```
Fix incorrect redirection after login

Previously, users were incorrectly redirected to the home page after
successful login, instead of the dashboard. This was due to an
incorrectly hardcoded path in the authentication service's redirect
logic.

This commit updates the redirect path to `/dashboard` to ensure users
land on the correct page post-login.

Fixes: #456
```

**New Feature:**
```
Add dark mode toggle to user settings

This introduces a new UI component for users to switch between light
and dark themes. The toggle persists the user's preference in local
storage and applies it globally using CSS variables.

Implements: #123
```

## Enforcement

CI lints every commit message on non-default, non-bot branches via
[`scripts/check-commit-messages.sh`](../scripts/check-commit-messages.sh)
(the `commit-message-standards` job in
[`run-linters.yml`](../.github/workflows/run-linters.yml)). It checks the
range from `git merge-base origin/main HEAD` to `HEAD`, so it only ever
lints your unpushed/unmerged commits.

Run it locally before pushing:

```bash
task lint:commits
```

This is a standalone task, deliberately not part of `task lint`: `task
lint` runs constantly during normal development and must not fail on
unpushed WIP commits.

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
| R14 | Issue-footer lines (`Closes`, `Fixes`, `Refs`, `Resolves`, `Implements`, `See also`) use the canonical `Keyword: #123[, #456]` form | FAIL |

`--strict` promotes WARN rules to FAIL; CI does not use it.

### Exemptions

Whole commits are skipped for:
- Merge commits.
- Bot authors (`*[bot]@users.noreply.github.com` or an author name ending in `[bot]`).
- Git-generated reverts (subject starts with `Revert "` -- a hand-written
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

## Rebasing Strategy

1. Develop on a feature branch created from `main`
2. Regularly `git pull --rebase origin main` to stay updated
3. Before merging, use `git rebase -i` to organize commits:
    - **Squash** related WIP commits
    - **Split** large commits into logical units
    - Each commit should be independently understandable

### Example Rebase Workflow

```bash
# Keep branch updated
git checkout 123-implement-dark-mode
git pull --rebase origin main

# Clean up commits before PR
git rebase -i HEAD~5

# Push after rebase
git push --force-with-lease origin 123-implement-dark-mode
```

## Best Practices

1. **Atomic Commits**: Each commit should represent one logical change
2. **Clear Messages**: Focus on WHY the change was made, not just what
3. **Issue References**: Always reference related issues in commit footers
4. **No WIP in Main**: Clean up WIP commits before merging

### Documenting Exceptions

Sometimes you may need to deviate from standards for valid reasons. In the PR description or comments, explain why:

- "Large commit acceptable because it's a vendored dependency update"
- "Branch name doesn't follow standard because it's a hotfix from production"
- "WIP commit kept for audit trail of security fix iterations"

## LLM-Assisted Commit Message Generation

You can use the following prompt with any LLM to generate conformant commit messages from your staged changes:

```
# Strict Commit Message Generator

## 1. System Role and Goal

You are a *highly disciplined* Git commit message generator. Your task is to create a commit message based on staged changes, strictly adhering to the classic format popularized by Tim Pope.

Your **primary goal** is to clearly explain the **WHY** behind a change and meticulously document all **BREAKING CHANGES**. The **WHAT** (the specific action) is secondary and should be summarized *exclusively* in the subject line.

## 2. Rules and Generation Process

When you analyze the staged changes, you MUST follow this process and set of rules:

### A. Determine Core Intent
1.  First, determine the core **action (WHAT)** of the change.
2.  Second, and most importantly, determine the primary **motivation (WHY)** for the change (the context, the problem being solved).

### B. Construct the Subject (The WHAT)
3.  Write a subject line that summarizes the **WHAT**.
4.  **STRICT: 50-Character Limit.**
5.  **STRICT: Use Imperative Mood** ("Fix bug", "Add feature").
6.  **STRICT: Capitalize** the first letter.
7.  **STRICT: NO period** at the end.
8.  **STRICT: NO prefixes** (e.g., `feat:`, `fix:`).

### C. Construct the Body (The WHY)
9.  **STRICT: Separate** subject from body with a **single blank line**.
10. **STRICT: Wrap all body lines at 72 characters.**
11. Focus the body *exclusively* on the **WHY** (motivation/context).
12. Use blank lines to separate logical paragraphs.

### D. Add TLDR (If Needed)
13. If the "WHY" explanation requires **more than two paragraphs**, you **MUST** include a short, bulleted list (`-`) at the top of the body (after the first blank line) to serve as an executive summary.

### E. Construct the Footer (Metadata & Breaking Changes)
14. **STRICT: Separate** body from footer with a **single blank line**.
15. **CRITICAL (PRIMARY GOAL):** If the change is non-backward compatible, you **MUST** identify it and add a `BREAKING CHANGE: <explanation>` section. The explanation must be detailed and include a migration path.
16. Add any issue references (e.g., `Closes: #123`).

## 3. CRITICAL: Output Rules
-   **ONLY** generate the complete, clean commit message.
-   **DO NOT** include *any* internal commentary, status, or conversational text.
-   **DO NOT** use any formatting other than the plain text specified.

## 4. Target Structure & Example

### Target Structure
~~~
<Capitalized, imperative subject (WHAT), 50 chars max>

<Optional bulleted summary (TLDR)>
<Only present if body is long/complex>

<Body text (WHY), wrapped at 72 chars>

BREAKING CHANGE: <detailed explanation> (Optional)

<Reference: #123> (Optional)
~~~

### Ideal Example
~~~
Centralize caches and update Docker base images

- Adopt shared base images for consistent Go and tool provisioning.
- Move Go and module caches to unified Docker volumes.
- Simplifies image maintenance and enables faster incremental builds.

The previous per-Dockerfile Go/tool provisioning led to version drift
and duplicated setup across various builder images (renovate, backend
builders, gateway). This makes builds inconsistent and harder to
maintain at scale.

This change moves all service definitions to derive from a single set
of base images, ensuring all environments use the same Go version and
tooling. It also unifies cache paths to enable faster, more reliable
incremental builds in CI.

BREAKING CHANGE: Go cache locations moved from /root/.cache to
/var/cache/go and /go/pkg/mod. Update Docker volumes accordingly.

Closes: #456
~~~

## 5. Input

${customInstructions}
${gitContext}
```

## Getting Help

If you're unsure about any of these standards, ask in our [Slack community](https://join.slack.com/t/pyck-community/shared_invite/zt-3ulnckg7r-kBk6Spkeyk_DldYQpDmcxA).