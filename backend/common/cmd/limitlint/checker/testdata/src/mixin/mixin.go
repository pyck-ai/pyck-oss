// Package mixin is a stub of backend/common/ent/mixin for analyzer
// tests. The schema files in testdata embed mixin.LimitMixin to mark
// the entity as having a 200-row cap.
package mixin

type Schema struct{}

type LimitMixin struct {
	Schema
}

type HistoryMixin struct {
	Schema
}
