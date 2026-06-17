package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
)

// IdempotencyKey holds idempotency records for GraphQL mutations served
// by the file service. The table is infrastructure managed entirely
// by the gqltx middleware: deliberately not exposed via GraphQL
// (entgql.SkipAll in the mixin), no privacy policy file, no Bruno
// fixtures, not part of the tenant-isolation test suite. See
// common/ent/mixin/idempotency_key_mixin.go for the full rationale and
// backend/common/idempotency for the behavior contract.
type IdempotencyKey struct {
	ent.Schema
}

func (IdempotencyKey) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Schema("file"),
		entsql.Table("idempotency_keys"),
	}
}

func (IdempotencyKey) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.IdempotencyKeyMixin{},
	}
}
