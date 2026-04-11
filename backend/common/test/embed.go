package test

import (
	"embed"
)

var (
	//go:embed jsonschemas/*
	schemaFS embed.FS
)
