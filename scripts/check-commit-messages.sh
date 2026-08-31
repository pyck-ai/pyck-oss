#!/bin/bash

# Script to lint commit message format against docs/COMMIT_STANDARDS.md
# (Tim Pope style: imperative subject <=50 chars, blank line, body wrapped
# at 72 chars, optional footer). Intended for CI, but safe to run locally.

set -euo pipefail  # Exit on error, undefined vars, pipe failures

# Global variables
SILENT=false
STRICT=false
BASE_REF="origin/main"
RANGE=""
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR/..")"
readonly MAX_COMMITS="${MAX_COMMITS:-100}"

TOTAL_FAIL=0
TOTAL_WARN=0
declare -A RULE_COUNTS=()
SUMMARY_HEADER_WRITTEN=false

# Per-commit violation accumulator; reset at the start of each check_commit call.
declare -a VIOLATIONS=()

# R7: imperative-mood denylist. Case-sensitive, first word only, must be
# followed by whitespace. "Revert", "Merge", "Test", "Release" are
# deliberately absent -- they collide with legitimate imperatives.
readonly -a IMPERATIVE_DENYLIST=(
  Added Fixed Updated Removed Changed Refactored Renamed Moved Bumped Implemented Deleted Created Improved
  Adds Fixes Updates Removes Changes Refactors Renames Moves Bumps Implements Deletes Creates Improves
  Adding Fixing Updating Removing Changing Refactoring Renaming Moving Bumping Implementing
)

# Regex patterns, one rule each. Kept in variables rather than written
# directly inside `[[ =~ ]]`: bash re-processes backslash escapes in an
# unquoted inline pattern before handing it to the regex engine, which
# silently breaks patterns like `^\ {1,3}\S` (matches nothing). Expanding
# an unquoted variable instead passes the pattern through untouched.
readonly RE_BOT_EMAIL='\[bot\]@users\.noreply\.github\.com$'
readonly RE_INDENTED_CODE='^ {4,}\S'
readonly RE_TABLE_ROW='^[[:space:]]*\|'
readonly RE_TRAILER='^[A-Za-z][A-Za-z0-9-]*: '
readonly RE_FENCE='^(```|~~~)'
readonly RE_BULLET_OPEN='^- \S'
readonly RE_BULLET_CONT='^  \S'
readonly RE_FLUSH_LEFT_VIOLATION='^ {1,3}\S'
readonly RE_EMPTY_SUBJECT='^[[:space:]]*$'
readonly RE_PR_SUFFIX='^(.*) \(#[0-9]+\)$'
readonly RE_CAPITALIZED='^[A-Z]'
readonly RE_CONVENTIONAL_PREFIX='^[a-z][a-z0-9]*(\([^)]*\))?!?: '
readonly RE_FIRST_WORD='^([A-Za-z]+)[[:space:]]'
readonly RE_FIXUP='^(fixup|squash)! '
readonly RE_MODULE_BUMP='^Update (module|.+ (Docker tag|action|monorepo|image)) '
readonly RE_FOOTER_KEYWORD='^(Closes|Fixes|Refs|Resolves|Implements|See also)($|[^A-Za-z])'
readonly RE_FOOTER_CANONICAL='^(Closes|Fixes|Refs|Resolves|Implements|See also): #[0-9]+(, ?#[0-9]+)*$'

# Log error message to stderr
# Args: error_message
function error() {
  echo >&2 "❌ $*"
}

# Log informational message (respects SILENT flag)
# Args: log_message
function log() {
  if [[ "$SILENT" == true ]]; then
    return
  fi
  echo "$*"
}

# Emit a GitHub Actions annotation for a single violation, when running in CI.
# Args: severity(FAIL|WARN) sha rule detail
function annotate() {
  local severity="$1" sha="$2" rule="$3" detail="$4"
  if [[ "${GITHUB_ACTIONS:-}" != "true" ]]; then
    return
  fi
  # No file=/line= -- a commit message violation has no source location.
  if [[ "$severity" == "FAIL" ]]; then
    echo "::error title=commit ${sha}: ${rule}::${detail}"
  else
    echo "::warning title=commit ${sha}: ${rule}::${detail}"
  fi
}

# Append the markdown table header to GITHUB_STEP_SUMMARY exactly once.
function ensure_summary_header() {
  if [[ "$SUMMARY_HEADER_WRITTEN" == true ]]; then
    return
  fi
  SUMMARY_HEADER_WRITTEN=true
  {
    echo "## Commit message check"
    echo ""
    echo "| sha | rule | detail |"
    echo "|-----|------|--------|"
  } >> "$GITHUB_STEP_SUMMARY"
}

# Record a violation for the commit currently being checked. In --strict
# mode, WARN severities are promoted to FAIL.
# Args: severity rule detail
function add_violation() {
  local severity="$1" rule="$2" detail="$3"
  if [[ "$STRICT" == true && "$severity" == "WARN" ]]; then
    severity="FAIL"
  fi
  VIOLATIONS+=("${severity}|${rule}|${detail}")
}

# Whether an author (name, email) pair identifies a bot, per the
# whole-commit skip list.
# Args: author_name author_email
function is_bot_author() {
  local an="$1" ae="$2"
  if [[ "$ae" =~ $RE_BOT_EMAIL ]]; then
    return 0
  fi
  if [[ "$an" == *"[bot]" ]]; then
    return 0
  fi
  return 1
}

# R11 exemptions: an over-length line is not a violation if any of these hold.
# Args: line in_fence(true|false)
function is_r11_exempt() {
  local line="$1" in_fence="$2"
  # 3. Inside a fenced code block (``` or ~~~).
  if [[ "$in_fence" == true ]]; then
    return 0
  fi
  # 4. Indented code block (4+ leading spaces).
  if [[ "$line" =~ $RE_INDENTED_CODE ]]; then
    return 0
  fi
  # 5. Table row.
  if [[ "$line" =~ $RE_TABLE_ROW ]]; then
    return 0
  fi
  # 2. Trailer line: key has no spaces. Exempts "Co-authored-by: ..." but
  # NOT "BREAKING CHANGE: ..." (space in key), which must still wrap.
  if [[ "$line" =~ $RE_TRAILER ]]; then
    return 0
  fi
  # 1. Unwrappable token: a single whitespace-delimited token >72 chars
  # (long URLs, identifiers, hashes).
  local token
  for token in $line; do
    if (( ${#token} > 72 )); then
      return 0
    fi
  done
  return 1
}

# Check the body lines (everything after the subject) for R9, R10, R11, R14.
# Args: sha, body_lines[@] passed via the global BODY_LINES array
function check_body() {
  local sha="$1"

  # R9: line 2 (index 1) must be blank if the message has more than one line.
  if [[ ${#BODY_LINES[@]} -ge 2 && -n "${BODY_LINES[1]}" ]]; then
    add_violation FAIL R9 "line 2 must be blank, found: '${BODY_LINES[1]}'"
  fi

  local in_bullet=false
  local in_fence=false
  local i line
  for ((i = 1; i < ${#BODY_LINES[@]}; i++)); do
    line="${BODY_LINES[$i]}"

    # Fence toggle (``` or ~~~), tracked for the R11 exemption only.
    if [[ "$line" =~ $RE_FENCE ]]; then
      if [[ "$in_fence" == true ]]; then in_fence=false; else in_fence=true; fi
      in_bullet=false
      continue
    fi

    # R10: body must be flush left, except bullet-list continuations
    # ("- foo" opens a block; subsequent "  bar" lines continue it).
    if [[ "$line" =~ $RE_BULLET_OPEN ]]; then
      in_bullet=true
    elif [[ -z "$line" ]]; then
      in_bullet=false
    elif [[ "$in_bullet" == true && "$line" =~ $RE_BULLET_CONT ]]; then
      : # allowed bullet continuation
    else
      in_bullet=false
      if [[ "$line" =~ $RE_FLUSH_LEFT_VIOLATION ]]; then
        add_violation FAIL R10 "body line is indented, must be flush left: '$line'"
      fi
    fi

    # R11: body lines wrapped at 72 chars, modulo exemptions.
    if (( ${#line} > 72 )) && ! is_r11_exempt "$line" "$in_fence"; then
      add_violation FAIL R11 "body line is ${#line} chars (max 72): '$line'"
    fi

    # R14: issue-footer lines must use the canonical "Keyword: #123" form.
    if [[ "$line" =~ $RE_FOOTER_KEYWORD && ! "$line" =~ $RE_FOOTER_CANONICAL ]]; then
      add_violation FAIL R14 "malformed issue footer, expected 'Keyword: #123[, #456]': '$line'"
    fi
  done
}

# Check the subject line for R1, R2, R4, R5, R6, R7, R8.
# Args: subject exempt_r2(true|false)
function check_subject() {
  local subject="$1" exempt_r2="$2"

  if [[ "$subject" =~ $RE_EMPTY_SUBJECT ]]; then
    add_violation FAIL R1 "subject line is empty"
    return
  fi

  # R2: length after stripping a trailing " (#123)" PR-merge suffix.
  local subj_for_len="$subject"
  if [[ "$subj_for_len" =~ $RE_PR_SUFFIX ]]; then
    subj_for_len="${BASH_REMATCH[1]}"
  fi
  if [[ "$exempt_r2" == false && ${#subj_for_len} -gt 50 ]]; then
    add_violation FAIL R2 "subject is ${#subj_for_len} chars (max 50): $subject"
  fi

  # R4: capitalized.
  if [[ ! "$subject" =~ $RE_CAPITALIZED ]]; then
    add_violation FAIL R4 "subject must start with a capital letter: $subject"
  fi

  # R5: no trailing period, except an ellipsis ("...").
  if [[ "$subject" == *"." && "$subject" != *"..." ]]; then
    add_violation FAIL R5 "subject must not end with a period: $subject"
  fi

  # R6: no conventional-commit prefix (feat:, fix(scope):, feat!: ...).
  if [[ "$subject" =~ $RE_CONVENTIONAL_PREFIX ]]; then
    add_violation FAIL R6 "subject must not use a conventional-commit prefix: $subject"
  fi

  # R7: imperative mood -- first word must not be past/3rd-person/gerund.
  if [[ "$subject" =~ $RE_FIRST_WORD ]]; then
    local first_word="${BASH_REMATCH[1]}"
    local denied
    for denied in "${IMPERATIVE_DENYLIST[@]}"; do
      if [[ "$first_word" == "$denied" ]]; then
        add_violation FAIL R7 "subject must use imperative mood, not '$first_word': $subject"
        break
      fi
    done
  fi

  # R8: fixup!/squash! commits must be autosquashed before merge.
  if [[ "$subject" =~ $RE_FIXUP ]]; then
    add_violation FAIL R8 "must be autosquashed (git rebase -i --autosquash) before merge: $subject"
  fi
}

# Check a single commit and report any violations. Skips the commit
# entirely (no violations, not counted) for bot authors and git-generated
# reverts, per the whole-commit exemption list. Merge commits are already
# excluded by the caller via `git rev-list --no-merges`.
# Args: sha
function check_commit() {
  local sha="$1"
  VIOLATIONS=()

  local an ae
  IFS=$'\x1f' read -r an ae < <(git log -1 --format='%an%x1f%ae' "$sha")
  if is_bot_author "$an" "$ae"; then
    return
  fi

  # `git log --format=%B` guarantees exactly two trailing newlines: one
  # from the commit object itself (git always terminates a commit message
  # with a single newline) and one row terminator that `git log` appends
  # after every formatted entry. Capture the output with a sentinel so
  # command substitution doesn't silently strip trailing newlines (which
  # would make R12's trailing-blank-line detection impossible), then strip
  # exactly those two guaranteed newlines.
  local raw message subject
  raw="$(git log -1 --format='%B' "$sha"; printf 'x')"
  raw="${raw%x}"
  message="${raw%$'\n'}"
  message="${message%$'\n'}"
  subject="${message%%$'\n'*}"

  # Git-generated revert: only the quoted form ('Revert "..."') is
  # git-authored. A bare "Revert foo" subject is hand-written and still
  # gets linted.
  if [[ "$subject" == 'Revert "'* ]]; then
    return
  fi

  # Rule-scoped exemption: machine-shaped Renovate dependency bump subjects
  # skip R2 only -- they can still fail R4/R5/R6/R7/etc.
  local exempt_r2=false
  if [[ "$subject" =~ $RE_MODULE_BUMP ]]; then
    exempt_r2=true
  fi

  check_subject "$subject" "$exempt_r2"

  # R12: any trailing newline left after stripping the one guaranteed by
  # %B indicates genuine trailing blank line(s) in the authored message.
  if [[ "$message" == *$'\n' ]]; then
    add_violation WARN R12 "message has trailing blank line(s)"
  fi

  local BODY_LINES=()
  mapfile -t BODY_LINES <<< "$message"
  check_body "$sha"

  if [[ ${#VIOLATIONS[@]} -eq 0 ]]; then
    return
  fi

  local short_sha
  short_sha="$(git rev-parse --short "$sha")"

  local v sev rule detail icon
  for v in "${VIOLATIONS[@]}"; do
    IFS='|' read -r sev rule detail <<< "$v"
    if [[ "$sev" == FAIL ]]; then
      TOTAL_FAIL=$((TOTAL_FAIL + 1))
      icon="❌"
    else
      TOTAL_WARN=$((TOTAL_WARN + 1))
      icon="⚠️"
    fi
    RULE_COUNTS["$rule"]=$(( ${RULE_COUNTS["$rule"]:-0} + 1 ))
    log "$icon $short_sha [$rule] $detail"
    annotate "$sev" "$short_sha" "$rule" "$detail"
    if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
      ensure_summary_header
      printf '| %s | %s | %s |\n' "$short_sha" "$rule" "$detail" >> "$GITHUB_STEP_SUMMARY"
    fi
  done

  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    {
      echo ""
      echo "<details><summary>Full message: ${short_sha}</summary>"
      echo ""
      echo '```'
      echo "$message"
      echo '```'
      echo "</details>"
      echo ""
    } >> "$GITHUB_STEP_SUMMARY"
  fi
}

# Decide what to pass to `git rev-list --no-merges --reverse` and store it
# in the global REV_LIST_ARGS array. Runs directly in the main shell (not
# inside a subshell/command substitution) so that `exit 2` here actually
# terminates the script -- exiting from inside `<(...)` or `$(...)` only
# ever kills the subshell, leaving the caller none the wiser (this was the
# root cause of the linter reporting a false pass on an unresolvable range).
# Args: range (may be empty, in which case it's computed from BASE_REF)
function compute_rev_list_args() {
  local range="$1"

  if [[ -n "$range" ]]; then
    REV_LIST_ARGS=("$range")
    return
  fi

  # Distinguish "BASE_REF doesn't exist" (shallow clone, missing fetch,
  # bad --base argument -- a script error) from "BASE_REF exists but
  # shares no history with HEAD" (a genuine orphan branch -- expected and
  # survivable). Conflating the two previously masked the far more likely
  # cause -- a missing/shallow origin/main -- behind a silent HEAD-only
  # fallback.
  if ! git rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
    error "base ref '$BASE_REF' not found -- is it fetched? (a shallow clone has no usable merge-base)"
    exit 2
  fi

  local merge_base
  if merge_base="$(git merge-base "$BASE_REF" HEAD 2>/dev/null)"; then
    REV_LIST_ARGS=("${merge_base}..HEAD")
  else
    # BASE_REF resolves but shares no common ancestor with HEAD: a real
    # orphan branch. Don't hard-fail the build over it, just lint the tip
    # commit and say why.
    log "⚠️  '$BASE_REF' and HEAD share no history (orphan branch?) -- linting HEAD only"
    REV_LIST_ARGS=(-1 HEAD)
  fi
}

# Display usage information
function usage() {
  cat << EOF
Usage: $(basename "$0") [OPTIONS] [<range>]

Lint commit messages in <range> against docs/COMMIT_STANDARDS.md.

  <range>        Git commit range to check (default: merge-base with
                 origin/main through HEAD)

OPTIONS:
  --silent       Suppress non-error output
  --base <ref>   Compute the merge-base against <ref> instead of origin/main
  --strict       Promote WARN-severity rules (currently R12) to FAIL
  --help         Show this help message

ENVIRONMENT:
  GITHUB_ACTIONS       When "true", also emit ::error/::warning annotations
  GITHUB_STEP_SUMMARY  When set, append a markdown report to this file
  MAX_COMMITS          Max commits to lint in one run (default: 100)

EXAMPLES:
  $(basename "$0")                  # Lint unpushed commits on the current branch
  $(basename "$0") --silent         # Same, minimal output (for CI/CD)
  $(basename "$0") HEAD~5..HEAD     # Lint an explicit range
  $(basename "$0") --strict         # Also fail on WARN-severity violations

EXIT CODES:
  0 - No FAIL-severity violations (WARNs allowed unless --strict)
  1 - One or more FAIL-severity violations found
  2 - Script error (not a repo, no origin/main, range too large, bad args)
EOF
}

# Parse command line arguments
function parse_args() {
  while [[ $# -gt 0 ]]; do
    case $1 in
      --silent)
        SILENT=true
        shift
        ;;
      --strict)
        STRICT=true
        shift
        ;;
      --base)
        if [[ $# -lt 2 ]]; then
          error "--base requires an argument"
          exit 2
        fi
        BASE_REF="$2"
        shift 2
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      -*)
        error "Unknown option: $1"
        usage >&2
        exit 2
        ;;
      *)
        if [[ -n "$RANGE" ]]; then
          error "Unexpected extra argument: $1"
          usage >&2
          exit 2
        fi
        RANGE="$1"
        shift
        ;;
    esac
  done
}

# Main function
function main() {
  parse_args "$@"

  if ! git rev-parse --git-dir >/dev/null 2>&1; then
    error "Not in a git repository"
    exit 2
  fi

  cd "$PROJECT_ROOT"

  local -a REV_LIST_ARGS=()
  compute_rev_list_args "$RANGE"

  # Capture rev-list's exit status explicitly rather than relying on
  # `set -e` to catch it: a failure here (bad range, unresolvable ref)
  # must be a script error, exit 2 -- never silently swallowed into an
  # empty commit list that then gets reported as a clean pass. A genuine
  # *success* that happens to return zero commits (e.g. HEAD is level
  # with the base) is not an error and must stay exit 0.
  local commits_raw rc
  commits_raw="$(git rev-list --no-merges --reverse "${REV_LIST_ARGS[@]}" 2>&1)" && rc=0 || rc=$?
  if (( rc != 0 )); then
    error "cannot resolve commit range '${REV_LIST_ARGS[*]}': $commits_raw"
    exit 2
  fi

  local -a commits=()
  if [[ -n "$commits_raw" ]]; then
    mapfile -t commits <<< "$commits_raw"
  fi

  if (( ${#commits[@]} > MAX_COMMITS )); then
    error "Refusing to lint ${#commits[@]} commits (MAX_COMMITS=$MAX_COMMITS)."
    error "Narrow the range, or raise MAX_COMMITS if this is intentional."
    exit 2
  fi

  log "Checking ${#commits[@]} commit(s) against docs/COMMIT_STANDARDS.md..."

  local sha
  for sha in "${commits[@]}"; do
    check_commit "$sha"
  done

  if (( TOTAL_FAIL > 0 || TOTAL_WARN > 0 )); then
    log ""
    log "Summary:"
    local rule
    for rule in "${!RULE_COUNTS[@]}"; do
      log "  $rule: ${RULE_COUNTS[$rule]}"
    done
  fi

  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]] && [[ "$SUMMARY_HEADER_WRITTEN" == true ]]; then
    {
      echo "**To fix:** rewrite the offending commit(s) and force-push:"
      echo ""
      echo '```'
      echo "git rebase -i $BASE_REF"
      echo "git push --force-with-lease"
      echo '```'
    } >> "$GITHUB_STEP_SUMMARY"
  fi

  if (( TOTAL_FAIL > 0 )); then
    error "Commit message check failed: $TOTAL_FAIL failing violation(s) across ${#commits[@]} commit(s)."
    exit 1
  fi

  if (( ${#commits[@]} == 0 )); then
    log "No commits in range -- nothing to check."
  else
    log "✅ Commit message check passed!"
  fi
}

# Only run main if script is executed directly (not sourced)
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
