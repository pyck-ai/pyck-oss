package main

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
)

func inputDef(name string, fieldNames ...string) *ast.Definition {
	fields := make(ast.FieldList, 0, len(fieldNames))
	for _, f := range fieldNames {
		fields = append(fields, &ast.FieldDefinition{Name: f})
	}
	return &ast.Definition{Kind: ast.InputObject, Name: name, Fields: fields}
}

func fieldNames(def *ast.Definition) []string {
	names := make([]string, 0, len(def.Fields))
	for _, f := range def.Fields {
		names = append(names, f.Name)
	}
	return names
}

// clearDataTypeID must be stripped from DataMixin entities' Update inputs:
// data_type_slug is server-derived from data_type_id and hidden from inputs,
// so exposing only the id's clear lets a client produce a row with a NULL
// data_type_id and a stale non-empty data_type_slug — exactly the shape the
// #990 precondition guard migration refuses to boot on. Re-pinning is done
// by SETTING a new id; there is no product reason to expose clearing.
func TestDropClearDataTypeID(t *testing.T) {
	t.Parallel()

	s := &ast.Schema{Types: map[string]*ast.Definition{
		"UpdateCustomerInput": inputDef("UpdateCustomerInput", "name", "dataTypeID", "clearDataTypeID", "data"),
		"CreateCustomerInput": inputDef("CreateCustomerInput", "name", "dataTypeID", "data"),
		// Not a DataMixin entity: an (hypothetical) unrelated clear field
		// with the same name must survive.
		"UpdateGadgetInput": inputDef("UpdateGadgetInput", "name", "clearDataTypeID"),
	}}

	dropClearDataTypeID(s, map[string]bool{"Customer": true})

	got := fieldNames(s.Types["UpdateCustomerInput"])
	for _, f := range got {
		if f == "clearDataTypeID" {
			t.Errorf("UpdateCustomerInput still exposes clearDataTypeID: %v", got)
		}
	}
	want := []string{"name", "dataTypeID", "data"}
	if len(got) != len(want) {
		t.Errorf("UpdateCustomerInput fields = %v, want %v", got, want)
	}

	if len(fieldNames(s.Types["CreateCustomerInput"])) != 3 {
		t.Errorf("CreateCustomerInput must be untouched: %v", fieldNames(s.Types["CreateCustomerInput"]))
	}
	if len(fieldNames(s.Types["UpdateGadgetInput"])) != 2 {
		t.Errorf("non-DataMixin UpdateGadgetInput must be untouched: %v", fieldNames(s.Types["UpdateGadgetInput"]))
	}
}
