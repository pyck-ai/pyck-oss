package services_test

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/workflow/services"
)

const (
	// deepNestingEnv carries the nesting depth into the re-executed test
	// binary; its presence tells the child test that it is the child.
	deepNestingEnv = "WF_FILTER_RULE_DEPTH"

	// stackKillingDepth is past the point where the FEEL parser's recursion
	// exhausts the goroutine stack (150 000 still parses, 200 000 dies).
	stackKillingDepth = 200_000

	// deepNestingSurvived is printed by the child only after EvalFilterRule
	// returned, so a child that never ran cannot pass the parent.
	deepNestingSurvived = "WF_DEEP_NESTING_SURVIVED"
)

func nestedParens(n int) string {
	return strings.Repeat("(", n) + "true" + strings.Repeat(")", n)
}

func nestedIfs(n int) string {
	return strings.Repeat("if true then ", n) + "true" + strings.Repeat(" else false", n)
}

// TestEvalFilterRuleSurvivesDeepNesting pins that a deeply nested rule is
// refused instead of recursing the parser into a stack overflow. A stack
// overflow is fatal in Go, so the rule is evaluated in a re-executed copy of
// this test binary: a crash there fails this test instead of the package.
func TestEvalFilterRuleSurvivesDeepNesting(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), os.Args[0],
		"-test.run=^TestEvalFilterRuleDeepNestingChild$", "-test.v")
	cmd.Env = append(os.Environ(), deepNestingEnv+"="+strconv.Itoa(stackKillingDepth))

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "a rule of %d nested parentheses killed the process:\n%s",
		stackKillingDepth, crashSummary(string(out)))
	require.Contains(t, string(out), deepNestingSurvived,
		"the child never evaluated the rule:\n%s", crashSummary(string(out)))
	assert.Contains(t, string(out), "longer than",
		"the child must report the rule as too long:\n%s", crashSummary(string(out)))
}

// TestEvalFilterRuleDeepNestingChild evaluates the deeply nested rule inside
// the re-executed binary. It is inert in a normal run.
func TestEvalFilterRuleDeepNestingChild(t *testing.T) {
	t.Parallel()

	depth := os.Getenv(deepNestingEnv)
	if depth == "" {
		t.Skip("child process of TestEvalFilterRuleSurvivesDeepNesting")
	}
	n, err := strconv.Atoi(depth)
	require.NoError(t, err)

	router := &services.SignalRouter{}
	ok, evalErr := router.EvalFilterRule(t.Context(), nestedParens(n), map[string]any{})
	t.Logf("depth %d: ok=%v err=%v", n, ok, evalErr)
	t.Log(deepNestingSurvived)
}

// crashSummary keeps a failure readable: a stack overflow prints a full
// goroutine dump, of which only the runtime's verdict matters.
func crashSummary(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "fatal error:") || strings.HasPrefix(line, "panic:") {
			return strings.Join(lines[i:min(i+6, len(lines))], "\n")
		}
	}
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	return strings.Join(lines, "\n")
}

// TestEvalFilterRuleRefusesUnboundedRules pins the limits that keep one rule's
// parse cost bounded: nesting through brackets or through keywords, and the
// rule's length.
func TestEvalFilterRuleRefusesUnboundedRules(t *testing.T) {
	t.Parallel()

	router := &services.SignalRouter{}
	tests := []struct {
		name    string
		rule    string
		wantMsg string
		wantErr error
	}{
		{"brackets past the nesting bound", nestedParens(129), "nests too deeply", services.ErrFilterRuleTooDeep},
		{"keywords past the nesting bound", nestedIfs(129), "nests too deeply", services.ErrFilterRuleTooDeep},
		{"list brackets past the nesting bound", strings.Repeat("[", 129) + "true" + strings.Repeat("]", 129), "nests too deeply", services.ErrFilterRuleTooDeep},
		{"context braces past the nesting bound", strings.Repeat("{a: ", 129) + "true" + strings.Repeat("}", 129), "nests too deeply", services.ErrFilterRuleTooDeep},
		{"for past the nesting bound", strings.Repeat("for x in [1] return ", 129) + "true", "nests too deeply", services.ErrFilterRuleTooDeep},
		{"some past the nesting bound", strings.Repeat("some x in [1] satisfies ", 129) + "true", "nests too deeply", services.ErrFilterRuleTooDeep},
		{"every past the nesting bound", strings.Repeat("every x in [1] satisfies ", 129) + "true", "nests too deeply", services.ErrFilterRuleTooDeep},
		{"function past the nesting bound", strings.Repeat("function(x) ", 129) + "true", "nests too deeply", services.ErrFilterRuleTooDeep},
		{"flat rule past the length bound", `status = "` + strings.Repeat("a", 5000) + `"`, "longer than", services.ErrFilterRuleTooLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ok, err := router.EvalFilterRule(t.Context(), tt.rule, map[string]any{"status": "a"})
			require.Error(t, err)
			assert.False(t, ok, "a refused rule must never match")
			assert.Contains(t, err.Error(), tt.wantMsg)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// TestEvalFilterRuleKeepsBoundedRules pins the floor of the limits: ordinary
// rules and nesting up to the bound still evaluate.
func TestEvalFilterRuleKeepsBoundedRules(t *testing.T) {
	t.Parallel()

	router := &services.SignalRouter{}
	data := map[string]any{"status": "active", "quantity": 150}
	tests := []struct {
		name string
		rule string
		want bool
	}{
		{"comparison", `status = "active" and quantity > 100`, true},
		{"no match", `status = "archived"`, false},
		{"brackets at the nesting bound", nestedParens(128), true},
		{"keywords at the nesting bound", nestedIfs(128), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ok, err := router.EvalFilterRule(t.Context(), tt.rule, data)
			require.NoError(t, err)
			assert.Equal(t, tt.want, ok)
		})
	}
}

// TestEvalFilterRuleReportsNonBooleanResult pins the refusal of a rule that
// evaluates to something other than a boolean: it must name the cause and wrap
// an error, not "%!w(<nil>)".
func TestEvalFilterRuleReportsNonBooleanResult(t *testing.T) {
	t.Parallel()

	router := &services.SignalRouter{}
	ok, err := router.EvalFilterRule(t.Context(), "quantity + 10", map[string]any{"quantity": 150})

	require.Error(t, err)
	assert.False(t, ok, "a rule that produced no boolean must never match")
	assert.NotContains(t, err.Error(), "%!w", "the error wraps nothing")
	assert.Contains(t, err.Error(), "boolean", "the error must say the rule produced no boolean")
	assert.ErrorIs(t, err, services.ErrFilterRuleNotBoolean)
}

// FuzzValidateFilterRule checks the invariants registration relies on: the
// validator never panics, an accepted rule is within the bounds, and the
// router can evaluate an accepted rule without hitting a bound.
func FuzzValidateFilterRule(f *testing.F) {
	for _, seed := range []string{
		"true", `status = "active" and quantity > 100`, "quantity + 10", "this is ((not feel",
		nestedParens(128), nestedParens(129), nestedIfs(128), "[{(", ")))", "if", "\x00", `"unterminated`,
	} {
		f.Add(seed)
	}
	router := &services.SignalRouter{}
	f.Fuzz(func(t *testing.T, rule string) {
		if err := services.ValidateFilterRule(rule); err != nil {
			return
		}
		if len(rule) > services.MaxFilterRuleBytes {
			t.Fatalf("accepted a rule of %d bytes", len(rule))
		}
		_, err := router.EvalFilterRule(t.Context(), rule, map[string]any{"status": "active"})
		if errors.Is(err, services.ErrFilterRuleTooLong) || errors.Is(err, services.ErrFilterRuleTooDeep) {
			t.Fatalf("accepted rule %q hit a bound at evaluation: %v", rule, err)
		}
	})
}

// TestEvalFilterRuleSurvivesLibraryPanics pins that a panic inside the FEEL
// evaluator becomes an error. The library panics on division and modulo by
// zero, and the divisor can come from the event, so an ordinary rule panics
// on the wrong payload; unrecovered, that ends the workflow service.
func TestEvalFilterRuleSurvivesLibraryPanics(t *testing.T) {
	t.Parallel()

	router := &services.SignalRouter{}
	tests := []struct {
		name string
		rule string
		data map[string]any
	}{
		{"modulo by zero", "1 % 0 = 0", nil},
		{"division by a zero field", "total / count > 5", map[string]any{"total": 10, "count": 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var (
				ok  bool
				err error
			)
			require.NotPanics(t, func() {
				ok, err = router.EvalFilterRule(t.Context(), tt.rule, tt.data)
			})
			require.Error(t, err)
			assert.False(t, ok, "a rule that failed to evaluate must never match")
			assert.ErrorIs(t, err, services.ErrFilterRuleEvalPanic)
		})
	}
}
