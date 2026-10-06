package main

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

func TestDeprecateWhereFields(t *testing.T) {
	t.Parallel()

	s := &ast.Schema{Types: map[string]*ast.Definition{
		"AWhereInput": inputDef("AWhereInput", "hasX", "hasXWith", "name"),
		"BWhereInput": inputDef("BWhereInput", "hasX"),
	}}

	deprecateWhereFields(s, map[string]map[string]string{
		"AWhereInput":       {"hasX": "because", "hasXWith": "because", "missing": "ignored"},
		"UnknownWhereInput": {"hasX": "ignored"},
	})
	// Idempotent: a second run must not stack a second directive.
	deprecateWhereFields(s, map[string]map[string]string{"AWhereInput": {"hasX": "because"}})

	a := s.Types["AWhereInput"]
	for _, name := range []string{"hasX", "hasXWith"} {
		f := a.Fields.ForName(name)
		if len(f.Directives) != 1 || f.Directives[0].Name != "deprecated" {
			t.Fatalf("%s directives = %v, want one @deprecated", name, f.Directives)
		}
		if got := f.Directives[0].Arguments.ForName("reason").Value.Raw; got != "because" {
			t.Errorf("%s reason = %q", name, got)
		}
	}
	if len(a.Fields.ForName("name").Directives) != 0 {
		t.Error("unlisted field must stay untouched")
	}
	if len(s.Types["BWhereInput"].Fields.ForName("hasX").Directives) != 0 {
		t.Error("unlisted type must stay untouched")
	}
}
