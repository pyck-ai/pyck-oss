// Command limitlint runs the limitlint analyzer over the given packages.
//
// Usage:
//
//	cd backend/<service>
//	go run github.com/pyck-ai/pyck/backend/common/cmd/limitlint ./...
//
// It auto-discovers LimitMixin entities by walking ./ent/schema/*.go
// relative to the working directory. Services without an ./ent/schema
// directory (e.g., common) yield an empty set, so the analyzer is a
// no-op there.
package main

import (
	"log"
	"os"

	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/pyck-ai/pyck/backend/common/cmd/limitlint/checker"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("limitlint: ")

	root, err := os.Getwd()
	if err != nil {
		log.Fatalf("cwd: %v", err)
	}

	entities, err := checker.DiscoverLimitMixinEntities(root)
	if err != nil {
		log.Fatalf("discover schemas: %v", err)
	}

	a := checker.New(checker.Config{
		IsLimitMixin: func(_, entity string) bool {
			return entities[entity]
		},
	})

	singlechecker.Main(a)
}
