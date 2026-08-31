package resolvers

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/db"

	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
)

// datatypeVersionConflict must translate a 23505 on the version unique index
// into db.ErrOCCConflict (so gqltx retries) for both driver error types, and
// pass everything else through unchanged.
func TestDatatypeVersionConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantOCC  bool
		wantSame bool // expect the exact same error back (pass-through)
	}{
		{
			name:     "nil passes through",
			err:      nil,
			wantSame: true,
		},
		{
			name:    "pq.Error 23505 on version index → OCC",
			err:     &pq.Error{Code: pgerrcode.UniqueViolation, Constraint: dataTypeVersionUniqueIndex},
			wantOCC: true,
		},
		{
			name:    "pgconn.PgError 23505 on version index → OCC",
			err:     &pgconn.PgError{Code: pgerrcode.UniqueViolation, ConstraintName: dataTypeVersionUniqueIndex},
			wantOCC: true,
		},
		{
			name:     "23505 on a different constraint passes through",
			err:      &pq.Error{Code: pgerrcode.UniqueViolation, Constraint: "datatype_some_other_index"},
			wantSame: true,
		},
		{
			name:     "non-unique pg error passes through",
			err:      &pgconn.PgError{Code: pgerrcode.ForeignKeyViolation, ConstraintName: dataTypeVersionUniqueIndex},
			wantSame: true,
		},
		{
			name:     "plain error passes through",
			err:      errors.New("boom"),
			wantSame: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := datatypeVersionConflict(tt.err)
			switch {
			case tt.wantOCC:
				require.ErrorIs(t, got, db.ErrOCCConflict)
			case tt.wantSame:
				assert.Equal(t, tt.err, got)
			}
		})
	}
}

// A wrapped 23505 (the real call site returns fmt.Errorf-wrapped driver errors
// in some paths) must still be detected via errors.As.
func TestDatatypeVersionConflict_Wrapped(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("save datatype: %w",
		&pq.Error{Code: pgerrcode.UniqueViolation, Constraint: dataTypeVersionUniqueIndex})
	require.ErrorIs(t, datatypeVersionConflict(wrapped), db.ErrOCCConflict)
}

// ptrTo builds the pointer the optional CreateDataTypeInput fields take; a nil
// there means "the importer did not supply this field".
func ptrTo[T any](v T) *T { return &v }

// immutableDataTypeFieldsDiffer decides whether a re-import of an existing
// (slug, version) is an idempotent no-op or a rejected rewrite of history.
// Every immutable field must be compared: a field silently dropped from the
// comparison lets a re-import mutate a version other rows are already pinned
// to, changing the schema underneath them without a version bump. The
// optional fields (description, frontendSchema, default) treat nil as
// "unspecified, therefore matching"; the required ones (jsonSchema, entity)
// are always compared, so an empty value is a genuine difference. slug and
// version are excluded on purpose — they are the identity the row was looked
// up by, so they can never differ here.
func TestImmutableDataTypeFieldsDiffer(t *testing.T) {
	t.Parallel()

	existing := &ent.DataType{
		Name:           "Item",
		Slug:           "item",
		Description:    "the item type",
		JSONSchema:     `{"type":"object"}`,
		FrontendSchema: `{"ui":"form"}`,
		Entity:         "item",
		Default:        true,
		Version:        3,
	}

	// matching is the full re-import of `existing`: every field supplied, all
	// equal. Each case starts from it and changes exactly one thing.
	matching := ent.CreateDataTypeInput{
		Name:           ptrTo("Item"),
		Slug:           ptrTo("item"),
		Description:    ptrTo("the item type"),
		JSONSchema:     `{"type":"object"}`,
		FrontendSchema: ptrTo(`{"ui":"form"}`),
		Entity:         "item",
		Default:        ptrTo(true),
		Version:        ptrTo(3),
	}

	mutate := func(f func(*ent.CreateDataTypeInput)) ent.CreateDataTypeInput {
		in := matching
		f(&in)
		return in
	}

	tests := []struct {
		name  string
		input ent.CreateDataTypeInput
		want  bool
	}{
		{
			name:  "identical re-import matches",
			input: matching,
		},
		{
			name:  "description differs",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Description = ptrTo("something else") }),
			want:  true,
		},
		{
			name:  "omitted description means unspecified, not cleared",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Description = nil }),
		},
		{
			name:  "jsonSchema differs",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.JSONSchema = `{"type":"string"}` }),
			want:  true,
		},
		{
			name:  "empty jsonSchema is a difference, not an omission",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.JSONSchema = "" }),
			want:  true,
		},
		{
			name:  "frontendSchema differs",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.FrontendSchema = ptrTo(`{"ui":"table"}`) }),
			want:  true,
		},
		{
			name:  "omitted frontendSchema means unspecified, not cleared",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.FrontendSchema = nil }),
		},
		{
			name:  "entity differs",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Entity = "customer" }),
			want:  true,
		},
		{
			name:  "empty entity is a difference, not an omission",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Entity = "" }),
			want:  true,
		},
		{
			name:  "default differs",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Default = ptrTo(false) }),
			want:  true,
		},
		{
			name:  "omitted default means unspecified, not cleared",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Default = nil }),
		},
		{
			name:  "name is mutable and never compared",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Name = ptrTo("Renamed") }),
		},
		{
			name:  "slug is the lookup identity and is excluded",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Slug = ptrTo("other") }),
		},
		{
			name:  "version is the lookup identity and is excluded",
			input: mutate(func(i *ent.CreateDataTypeInput) { i.Version = ptrTo(99) }),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, immutableDataTypeFieldsDiffer(existing, tt.input))
		})
	}
}
