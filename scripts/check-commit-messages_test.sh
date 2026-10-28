#!/bin/bash

# Self-contained test for the R14 (issue trailer) check in
# check-commit-messages.sh. Builds a throwaway repo, commits one message per
# case and asserts pass or fail. Exits non-zero on any mismatch.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
SANDBOX="$(mktemp -d)"
readonly SANDBOX
trap 'rm -rf "$SANDBOX"' EXIT

# The linter runs git from the toplevel of the repo that contains the script,
# so copy it into the sandbox repo.
mkdir "$SANDBOX/scripts"
cp "$SCRIPT_DIR/check-commit-messages.sh" "$SANDBOX/scripts/"

cd "$SANDBOX"
git init -q -b main
git config user.name "Test"
git config user.email "test@example.com"
git config commit.gpgsign false
git commit -q --allow-empty -m "Initial commit"

PASSED=0
FAILED=0

# Args: name expect(pass|fail) message
function run_case() {
  local name="$1" expect="$2" message="$3" out rc=0
  git checkout -q -B case main
  git commit -q --allow-empty -m "$message"
  out="$(./scripts/check-commit-messages.sh --base main 2>&1)" || rc=$?
  local actual=pass
  if (( rc == 1 )) && [[ "$out" == *"[R14]"* ]]; then
    actual=fail
  elif (( rc != 0 )); then
    actual="error(rc=$rc)"
  fi
  if [[ "$actual" == "$expect" ]]; then
    PASSED=$((PASSED + 1))
    echo "ok   $name"
  else
    FAILED=$((FAILED + 1))
    echo "FAIL $name: expected $expect, got $actual"
    printf '     %s\n' "${out//$'\n'/$'\n     '}"
  fi
}

function msg() { # subject, then trailer/body lines after a blank line
  local IFS=$'\n'
  printf 'Add the thing\n\n%s' "$*"
}

# Allowed forms.
run_case "Closes: #1" pass "$(msg 'Closes: #1')"
run_case "Part-of: #2" pass "$(msg 'Part-of: #2')"
run_case "See-also: #3" pass "$(msg 'See-also: #3')"
run_case "Closes: pyck-ai/guidelines#4" pass "$(msg 'Closes: pyck-ai/guidelines#4')"
run_case "two Closes lines" pass "$(msg 'Closes: #1' 'Closes: #2')"
run_case "Co-authored-by" pass "$(msg 'Co-authored-by: X <x@y>')"
run_case "subject only" pass "Add the thing"

# Legacy keywords in trailer shape.
for t in 'Fixes: #1' 'Fixes #1' 'Refs: #1' 'Resolves #2' 'Implements: #3' \
  'Related: #4' 'See also: #5' 'See: #6' 'Fix: #7' 'Ref: #8' 'Resolve: #9'; do
  run_case "legacy $t" fail "$(msg "$t")"
done

# Malformed allowed trailers.
run_case "Closes: #1, #2" fail "$(msg 'Closes: #1, #2')"
run_case "Closes #1" fail "$(msg 'Closes #1')"
run_case "Closes:#1" fail "$(msg 'Closes:#1')"
run_case "Part-of: #1 and #2" fail "$(msg 'Part-of: #1 and #2')"

# Prose is not a trailer.
run_case "prose Fixes the race" pass "$(msg 'Fixes the race in X')"
run_case "prose Closes the gap" pass "$(msg 'Closes the gap between A and B')"
run_case "prose See also the ADR" pass "$(msg 'See also the ADR')"
run_case "prose Refs are resolved" pass "$(msg 'Refs are resolved lazily')"

echo "$PASSED passed, $FAILED failed"
(( FAILED == 0 ))
