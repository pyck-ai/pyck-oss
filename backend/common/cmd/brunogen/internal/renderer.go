package gen

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"

	"github.com/pyck-ai/pyck/backend/common/cmd/brunogen/types"
)

const (
	kindRes      = "res"
	kindReq      = "req"
	subStatus    = "status"
	refResStatus = "res.status"
)

// RenderedExtract is a bru.setVar call to persist a response value for
// subsequent steps in the scenario.
type RenderedExtract struct {
	VarName   string // collection variable name, e.g. "bru-test_createfile_1a2b3c4d"
	Expr      string // JS expression to evaluate, e.g. "body.data.foo.id"
	NeedsBody bool   // true when Expr references the `body` variable
	NeedsVars bool   // true when Expr references the `vars` variable
}

// RenderedTest is one test() block ready for template rendering.
type RenderedTest struct {
	QuotedName string // JS string literal, e.g. `"Status is 200"`
	Expects    []RenderedExpect
}

// RenderedExpect is a single expect() call within a test() block.
type RenderedExpect struct {
	Subject   string // JS expression for non-wildcard paths
	Assertion string // Chai chain, e.g. ".to.equal(200)"
	// Wildcard paths expand to a forEach loop:
	Wildcard   bool
	ArrayExpr  string // JS expression for the array
	ItemPath   string // dotted sub-path within each element
	ItemAssert string // Chai chain for the sub-path
}

// ResolveSkip renders skip assertion blocks into RenderedTest values for the
// before-request template. Each RenderedExpect in the result becomes its own
// try { expect(...); bru.runner.skipRequest(); } catch(e) {} block, so the
// request is skipped when at least one assertion passes.
func ResolveSkip(skip []types.TestAssertion, varNS string) []RenderedTest {
	if len(skip) == 0 {
		return nil
	}
	checks, _, _ := renderAssertionBlocks(skip, varNS)
	return checks
}

// CollectExtracts scans all step vars and assertion args for cross-step $ref
// patterns and returns a map from step ID → []RenderedExtract.
func CollectExtracts(steps []types.TestStep, varNS string) map[string][]RenderedExtract {
	seen := make(map[string]map[string]bool)
	result := make(map[string][]RenderedExtract)

	add := func(id, varName, expr string, needsBody, needsVars bool) {
		if seen[id] == nil {
			seen[id] = make(map[string]bool)
		}
		if seen[id][varName] {
			return
		}
		seen[id][varName] = true
		result[id] = append(result[id], RenderedExtract{
			VarName: varName, Expr: expr,
			NeedsBody: needsBody, NeedsVars: needsVars,
		})
	}

	var scan func(any)
	scan = func(v any) {
		switch val := v.(type) {
		case map[string]any:
			if ref, ok := val["$ref"]; ok {
				refStr := fmt.Sprintf("%v", ref)
				if rp, ok := parseRef(refStr); ok && rp.id != "" {
					varName := collectionVarName(varNS, refStr)
					expr, needsBody, needsVars := extractJSExpr(rp)
					add(rp.id, varName, expr, needsBody, needsVars)
				}
				return
			}
			for _, child := range val {
				scan(child)
			}
		case []any:
			for _, item := range val {
				scan(item)
			}
		}
	}

	for _, step := range steps {
		for _, block := range step.Skip {
			for _, a := range block.Assertions {
				if rp, ok := parseRef(a.Ref); ok && rp.id != "" {
					varName := collectionVarName(varNS, a.Ref)
					expr, needsBody, needsVars := extractJSExpr(rp)
					add(rp.id, varName, expr, needsBody, needsVars)
				}
				scan(a.Args)
			}
		}
		for _, v := range step.Vars {
			scan(v)
		}
		for _, block := range step.Tests {
			for _, a := range block.Assertions {
				scan(a.Args)
			}
		}
	}
	// scan walks map[string]any children in Go's randomized iteration order, so
	// sort each step's extracts by VarName (unique per step) for stable output.
	for _, extracts := range result {
		sort.Slice(extracts, func(i, j int) bool {
			return extracts[i].VarName < extracts[j].VarName
		})
	}
	return result
}

// ProcessStep converts a TestStep into Bruno template data.
func ProcessStep(step types.TestStep, varNS string) ([]RenderedTest, bool, bool) {
	return renderAssertionBlocks(step.Tests, varNS)
}

// ProcessExampleScenario converts an ExampleScenario into Bruno template data.
func ProcessExampleScenario(scenario types.ExampleScenario) ([]RenderedTest, bool, bool) {
	return renderAssertionBlocks(scenario.Expect, "")
}

// MarshalVarsFor resolves $fake/$ref/$placeholder values and serialises vars as
// 2-space indented JSON with numeric dynamic variables unquoted and placeholder
// entries rendered as null with an inline hint comment.
func MarshalVarsFor(vars map[string]any, varNS string) (string, error) {
	b, err := json.MarshalIndent(resolveVars(vars, varNS), "", "  ")
	if err != nil {
		return "", err
	}
	s := quotedNumericBrunoRe.ReplaceAllString(string(b), "$1")
	s = placeholderRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := placeholderRe.FindStringSubmatch(match)
		hint, comma := sub[1], sub[2]
		if hint == "" {
			return "null" + comma + " // PLACEHOLDER:"
		}
		return "null" + comma + " // PLACEHOLDER: " + hint
	})
	return s, nil
}

// placeholderValue holds the hint string from a $placeholder directive.
// MarshalJSON encodes it as a sentinel string so that placeholderRe can
// replace the whole value with: null[,] // PLACEHOLDER: <hint>
type placeholderValue string

func (p placeholderValue) MarshalJSON() ([]byte, error) {
	return json.Marshal("__placeholder__:" + string(p))
}

// placeholderRe matches a sentinel placeholder value produced by MarshalJSON
// and captures (hint, optional-comma) so the replacement puts the comma before
// the comment: null, // PLACEHOLDER: hint
var placeholderRe = regexp.MustCompile(`"__placeholder__:([^"]*)"(,?)`)

// quotedNumericBrunoRe strips quotes around numeric Bruno dynamic variables
// that json.MarshalIndent adds.
var quotedNumericBrunoRe = func() *regexp.Regexp {
	seen := make(map[string]bool)
	var alts []string
	for _, e := range fakeTypes {
		if e.Numeric && !seen[e.Bruno] {
			seen[e.Bruno] = true
			alts = append(alts, regexp.QuoteMeta(e.Bruno))
		}
	}
	return regexp.MustCompile(`"(` + strings.Join(alts, "|") + `)"`)
}()

// --- collection variable naming ---

// collectionVarName generates a stable Bruno collection variable name for a
// cross-step $ref.  Format: <varNS>_<8hexhash>
// varNS already contains the prefix, e.g. "bru-test_myscenario" or "bru-example".
func collectionVarName(varNS, refStr string) string {
	h := fnv.New32a()
	h.Write([]byte(refStr))
	return fmt.Sprintf("%s_%08x", varNS, h.Sum32())
}

func resolveVars(m map[string]any, varNS string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = resolveVarValue(v, varNS)
	}
	return out
}

func resolveVarValue(v any, varNS string) any {
	if list, ok := v.([]any); ok {
		out := make([]any, len(list))
		for i, item := range list {
			out[i] = resolveVarValue(item, varNS)
		}
		return out
	}
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if pv, ok := m["$placeholder"]; ok {
		if pv == nil {
			return placeholderValue("")
		}
		return placeholderValue(fmt.Sprintf("%v", pv))
	}
	if fake, ok := m["$fake"]; ok {
		return resolveFakeTemplate(fmt.Sprintf("%v", fake), varNS)
	}
	if ref, ok := m["$ref"]; ok {
		refStr := fmt.Sprintf("%v", ref)
		if rp, ok := parseRef(refStr); ok && rp.id != "" {
			return "{{" + collectionVarName(varNS, refStr) + "}}"
		}
		return "{{" + refStr + "}}"
	}
	return resolveVars(m, varNS)
}

// --- $ref → JS expression conversion ---

// optChain converts a dot-separated JS path to use optional chaining so that
// mid-path nulls produce undefined rather than a TypeError.
// e.g. "body.data.foo.id" → "body?.data?.foo?.id"
func optChain(path string) string {
	return strings.ReplaceAll(path, ".", "?.")
}

func refPathToJS(rp refPath) string {
	switch {
	case rp.kind == kindRes && rp.subpath == subStatus:
		return refResStatus
	case rp.kind == kindRes && strings.HasPrefix(rp.subpath, "headers."):
		return "res.headers['" + strings.TrimPrefix(rp.subpath, "headers.") + "']"
	case rp.kind == kindRes && strings.HasPrefix(rp.subpath, "body."):
		return optChain(rp.subpath)
	case rp.kind == kindReq && strings.HasPrefix(rp.subpath, "body.variables."):
		return "vars." + strings.TrimPrefix(rp.subpath, "body.variables.")
	default:
		return "undefined /* unsupported ref: " + rp.kind + "." + rp.subpath + " */"
	}
}

func extractJSExpr(rp refPath) (expr string, needsBody, needsVars bool) {
	switch {
	case rp.kind == kindRes && rp.subpath == subStatus:
		return refResStatus, false, false
	case rp.kind == kindRes && strings.HasPrefix(rp.subpath, "headers."):
		return "res.headers['" + strings.TrimPrefix(rp.subpath, "headers.") + "']", false, false
	case rp.kind == kindRes && strings.HasPrefix(rp.subpath, "body."):
		return optChain(rp.subpath), true, false
	case rp.kind == kindReq && strings.HasPrefix(rp.subpath, "body.variables."):
		return "vars." + strings.TrimPrefix(rp.subpath, "body.variables."), false, true
	default:
		return "undefined /* unsupported: " + rp.kind + "." + rp.subpath + " */", false, false
	}
}

func renderArgs(args any, varNS string) string {
	switch v := args.(type) {
	case map[string]any:
		if ref, ok := v["$ref"]; ok {
			refStr := fmt.Sprintf("%v", ref)
			if rp, ok := parseRef(refStr); ok {
				if rp.id != "" {
					return "bru.getVar('" + collectionVarName(varNS, refStr) + "')"
				}
				return refPathToJS(rp)
			}
			return "bru.getVar('" + fmt.Sprintf("%v", ref) + "')"
		}
		return "null"
	case string:
		return "'" + strings.ReplaceAll(v, "'", `\'`) + "'"
	case int, int8, int16, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%v", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("'%v'", v)
	}
}

func chaiChain(test string, args any, varNS string) string {
	switch test {
	case "equal":
		return ".to.equal(" + renderArgs(args, varNS) + ")"
	case "notEqual":
		return ".to.not.equal(" + renderArgs(args, varNS) + ")"
	case "nil":
		if v, ok := args.(bool); ok && !v {
			return ".to.exist"
		}
		return ".to.not.exist"
	case "empty":
		if v, ok := args.(bool); ok && !v {
			return ".to.not.be.empty"
		}
		return ".to.be.empty"
	case "contains":
		return ".to.include(" + renderArgs(args, varNS) + ")"
	case "isType":
		return ".to.be.a(" + renderArgs(args, varNS) + ")"
	case "greater":
		return ".to.be.greaterThan(" + renderArgs(args, varNS) + ")"
	case "less":
		return ".to.be.lessThan(" + renderArgs(args, varNS) + ")"
	case "greaterOrEqual":
		return ".to.be.gte(" + renderArgs(args, varNS) + ")"
	case "lessOrEqual":
		return ".to.be.lte(" + renderArgs(args, varNS) + ")"
	case "len":
		return ".to.have.lengthOf(" + renderArgs(args, varNS) + ")"
	case "regexp":
		return ".to.match(new RegExp(" + renderArgs(args, varNS) + "))"
	case "exists":
		if v, ok := args.(bool); ok && !v {
			return ".to.not.exist"
		}
		return ".to.exist"
	default:
		return fmt.Sprintf("/* UNSUPPORTED: %s */", test)
	}
}

type parsedPath struct {
	source       string
	headerKey    string
	bodyPath     string
	wildcard     bool
	arrayExpr    string
	itemPath     string
	crossStepVar string // non-empty for cross-step refs; use bru.getVar(crossStepVar)
}

var arrayWildcardRe = regexp.MustCompile(`^(.*)\[\]\.(.+)$`)

// parseAssertionRef parses a ref string from an assertion, handling both
// same-step paths (res.body.x, res.status, ...) and cross-step refs
// (res[stepID].body.x) which resolve to a bru collection variable.
func parseAssertionRef(ref string, varNS string) parsedPath {
	if rp, ok := parseRef(ref); ok && rp.id != "" {
		return parsedPath{crossStepVar: collectionVarName(varNS, ref)}
	}
	return parsePath(ref)
}

func parsePath(path string) parsedPath {
	switch {
	case path == refResStatus:
		return parsedPath{source: subStatus}
	case strings.HasPrefix(path, "res.headers."):
		return parsedPath{source: "headers", headerKey: strings.TrimPrefix(path, "res.headers.")}
	case strings.HasPrefix(path, "res.body."):
		bodyPath := strings.TrimPrefix(path, "res.body.")
		if m := arrayWildcardRe.FindStringSubmatch(bodyPath); m != nil {
			return parsedPath{
				source: "body", bodyPath: bodyPath, wildcard: true,
				arrayExpr: "body." + m[1], itemPath: m[2],
			}
		}
		return parsedPath{source: "body", bodyPath: bodyPath}
	default:
		return parsedPath{source: "body", bodyPath: path}
	}
}

func jsSubject(pp parsedPath) string {
	if pp.crossStepVar != "" {
		return "bru.getVar('" + pp.crossStepVar + "')"
	}
	switch pp.source {
	case subStatus:
		return refResStatus
	case "headers":
		return "res.headers['" + pp.headerKey + "']"
	default:
		return optChain("body." + pp.bodyPath)
	}
}

func jsQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func isVarsRef(args any) bool {
	m, ok := args.(map[string]any)
	if !ok {
		return false
	}
	ref, ok := m["$ref"]
	if !ok {
		return false
	}
	rp, ok := parseRef(fmt.Sprintf("%v", ref))
	return ok && rp.id == "" && rp.kind == kindReq && strings.HasPrefix(rp.subpath, "body.variables.")
}

func isBodyRef(args any) bool {
	m, ok := args.(map[string]any)
	if !ok {
		return false
	}
	ref, ok := m["$ref"]
	if !ok {
		return false
	}
	rp, ok := parseRef(fmt.Sprintf("%v", ref))
	return ok && rp.id == "" && rp.kind == kindRes && strings.HasPrefix(rp.subpath, "body.")
}

func renderAssertionBlocks(blocks []types.TestAssertion, varNS string) ([]RenderedTest, bool, bool) {
	tests := make([]RenderedTest, 0, len(blocks))
	useBody, useReqVars := false, false

	for i, block := range blocks {
		var expects []RenderedExpect
		for _, a := range block.Assertions {
			pp := parseAssertionRef(a.Ref, varNS)
			if pp.source == "body" {
				useBody = true
			}
			if isVarsRef(a.Args) {
				useReqVars = true
			}
			if isBodyRef(a.Args) {
				useBody = true
			}
			var re RenderedExpect
			if pp.wildcard {
				re.Wildcard = true
				re.ArrayExpr = optChain(pp.arrayExpr)
				re.ItemPath = optChain(pp.itemPath)
				re.ItemAssert = chaiChain(a.Test, a.Args, varNS)
			} else {
				re.Subject = jsSubject(pp)
				re.Assertion = chaiChain(a.Test, a.Args, varNS)
			}
			expects = append(expects, re)
		}
		name := block.Msg
		if name == "" {
			name = fmt.Sprintf("test %d", i+1)
		}
		tests = append(tests, RenderedTest{QuotedName: jsQuote(name), Expects: expects})
	}
	return tests, useBody, useReqVars
}
