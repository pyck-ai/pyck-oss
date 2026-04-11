package main

import (
	"embed"

	"entgo.io/contrib/entgql"
	"entgo.io/ent/entc"
	ent "entgo.io/ent/entc/gen"
)

// jsonbExtension is an entc.Extension that adds JSONB ordering support
// to the generated pagination code. It:
//  1. Registers a gen.Hook that renames upstream pagination methods via AST
//     post-processing so our custom template can override them.
//  2. Provides a template that generates JSONB-aware override methods.
type jsonbExtension struct {
	entc.DefaultExtension
}

//go:embed templates/gql/*
var gqlTemplateFS embed.FS

func (jsonbExtension) Hooks() []ent.Hook {
	return []ent.Hook{jsonbRenameHook()}
}

func (jsonbExtension) Templates() []*ent.Template {
	t, err := ent.NewTemplate("gql_extension").
		Funcs(entgql.TemplateFuncs).
		ParseFS(gqlTemplateFS, "templates/gql/*.tmpl")
	if err != nil {
		// Templates are embedded; parse failure is a build-time bug.
		panic("gql extension templates: " + err.Error())
	}
	return []*ent.Template{t}
}
