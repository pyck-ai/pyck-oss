package services

import (
	"errors"
	"fmt"

	"github.com/pbinitiative/feel"
)

const (
	// MaxFilterRuleBytes bounds a filter rule's length, and with it the cost
	// of parsing the rule on every matching event. Real rules are a few dozen
	// bytes.
	MaxFilterRuleBytes = 4096

	// MaxFilterRuleNesting bounds how deeply a filter rule nests. The FEEL
	// parser is recursive descent: every nesting level is a chain of Go calls,
	// and running out of stack is a fatal error that no recover catches.
	MaxFilterRuleNesting = 128
)

var (
	// ErrFilterRuleTooLong reports a rule longer than MaxFilterRuleBytes.
	ErrFilterRuleTooLong = errors.New("filter rule is too long")

	// ErrFilterRuleTooDeep reports a rule nested deeper than
	// MaxFilterRuleNesting.
	ErrFilterRuleTooDeep = errors.New("filter rule nests too deeply")

	// ErrFilterRuleNotBoolean reports a rule that evaluated to something other
	// than a boolean.
	ErrFilterRuleNotBoolean = errors.New("filter rule does not evaluate to a boolean")

	// ErrFilterRuleEvalPanic reports a panic inside the FEEL evaluator, which
	// panics on division and modulo by zero.
	ErrFilterRuleEvalPanic = errors.New("filter rule evaluation panicked")
)

// ValidateFilterRule reports whether rule can be stored on a workflow signal:
// it must fit the length and nesting bounds and parse as FEEL.
func ValidateFilterRule(rule string) error {
	_, err := parseFilterRule(rule)
	return err
}

// parseFilterRule checks the bounds before the recursive parser sees the rule.
func parseFilterRule(rule string) (feel.Node, error) { //nolint:ireturn // the library's AST is an interface
	if len(rule) > MaxFilterRuleBytes {
		return nil, fmt.Errorf("%w: %d bytes, longer than %d", ErrFilterRuleTooLong, len(rule), MaxFilterRuleBytes)
	}
	if err := checkFilterRuleNesting(rule); err != nil {
		return nil, err
	}
	node, err := feel.ParseString(rule)
	if err != nil {
		return nil, fmt.Errorf("failed to parse FEEL expression %q: %w", rule, err)
	}
	return node, nil
}

// checkFilterRuleNesting bounds nesting with the library's scanner, a loop
// that does not recurse on nesting. FEEL nests through brackets and through
// the keywords that open a sub-expression. Brackets close, so their depth is
// tracked; a keyword's sub-expression has no closing token the scanner can
// see, so keywords are counted in total, which bounds how deeply they can
// nest and also refuses a rule with that many keywords side by side. A rule
// the scanner cannot tokenise is left to the parser, which reports it.
func checkFilterRuleNesting(rule string) error {
	sc := feel.NewScanner(rule)
	depth, keywords := 0, 0
	for sc.Next() == nil {
		tok := sc.Current()
		switch tok.Kind {
		case feel.TokenEOF:
			return nil
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			depth--
		case feel.TokenKeyword:
			if tok.ExpectKeywords("if", "for", "some", "every", "function") {
				keywords++
			}
		}
		if depth > MaxFilterRuleNesting || keywords > MaxFilterRuleNesting {
			return fmt.Errorf("%w: more than %d levels", ErrFilterRuleTooDeep, MaxFilterRuleNesting)
		}
	}
	return nil
}

// evalFilterRule evaluates a parsed rule. The FEEL evaluator panics on some
// inputs (division and modulo by zero, with a divisor that may come from the
// event), and the router's NATS handlers run without a recover, so a panic
// here would end the workflow service for every tenant.
func evalFilterRule(node feel.Node, interpreter *feel.Interpreter) (result any, err error) {
	defer func() {
		if p := recover(); p != nil {
			result, err = nil, fmt.Errorf("%w: %v", ErrFilterRuleEvalPanic, p)
		}
	}()
	return node.Eval(interpreter)
}
