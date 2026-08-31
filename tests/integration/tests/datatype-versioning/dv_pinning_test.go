//go:build integration

package datatypeversioning

import (
	"fmt"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TestEntityPinLifecycle: an entity pins one exact DataType version by id;
// the server derives the slug; publishing a newer version never re-points
// or re-validates the entity; validation always runs against the pinned
// version; and a client can re-pin explicitly to adopt the new version.
func (s *VersioningSuite) TestEntityPinLifecycle() {
	r := s.Require()
	tc := s.newTenantClient()
	slug := dvSlug("pin")

	v1in := newDataTypeInput(slug)
	v1in.JSONSchema = strictZoneSchema
	v1 := tc.mustCreateDT(s, v1in)
	v1id := mustUUID(s, v1.ID)

	loc, err := tc.createLocation(s, &v1id, map[string]any{"zone": "A"})
	r.NoError(err, "create pinned to v1 with valid data")
	r.NotNil(loc.DataTypeID)
	r.Equal(v1.ID, loc.DataTypeID.String(), "entity pins the exact version id")
	r.NotNil(loc.DataTypeSlug)
	r.Equal(slug, *loc.DataTypeSlug, "slug is server-derived from the pin")

	// Publish v2 with an evolved schema. The existing entity stays pinned.
	v2in := newDataTypeInput(slug)
	v2in.JSONSchema = zoneFloorSchema
	v2 := tc.mustCreateDT(s, v2in)
	v2id := mustUUID(s, v2.ID)
	r.Equal(2, v2.Version)

	// Updates keep validating against the PINNED v1 schema: data that only
	// v2 accepts (extra "floor") is rejected while the pin is v1...
	_, err = tc.updateLocation(s, loc.ID, &v1id, map[string]any{"zone": "B", "floor": float64(2)})
	r.Error(err, "v2-shaped data must fail against the pinned v1 schema")
	r.ErrorContains(err, "jsonschema validation failed")

	// ...and v1-shaped data still passes, even though v2 is now latest.
	updated, err := tc.updateLocation(s, loc.ID, &v1id, map[string]any{"zone": "B"})
	r.NoError(err, "v1-shaped data stays valid while pinned to v1")
	r.Equal(v1.ID, updated.DataTypeID.String(), "publishing v2 does not re-point the entity")

	// Explicit re-pin to v2 with v2-shaped data adopts the new version.
	repinned, err := tc.updateLocation(s, loc.ID, &v2id, map[string]any{"zone": "B", "floor": float64(2)})
	r.NoError(err, "explicit re-pin to v2")
	r.Equal(v2.ID, repinned.DataTypeID.String())
	r.NotNil(repinned.DataTypeSlug)
	r.Equal(slug, *repinned.DataTypeSlug, "slug is stable across re-pins within the family")
}

// TestPinValidationErrors: every dataTypeID mismatch mode on an entity
// write fails with a precise error — data without a pin, a pin that does
// not exist, a pin to a soft-deleted version, data violating the pinned
// schema, and an update that carries data without re-sending the pin.
func (s *VersioningSuite) TestPinValidationErrors() {
	r := s.Require()
	tc := s.newTenantClient()

	v1in := newDataTypeInput(dvSlug("pin-err"))
	v1in.JSONSchema = strictZoneSchema
	v1 := tc.mustCreateDT(s, v1in)
	v1id := mustUUID(s, v1.ID)

	s.Run("data without dataTypeID", func() {
		_, err := tc.createLocation(s, nil, map[string]any{"zone": "A"})
		r.Error(err)
		r.ErrorContains(err, "data type not set")
	})

	s.Run("nonexistent dataTypeID", func() {
		ghost := uuid.Must(uuid.NewV7())
		_, err := tc.createLocation(s, &ghost, map[string]any{"zone": "A"})
		r.Error(err)
		r.ErrorContains(err, "data type not found")
	})

	s.Run("data violating the pinned schema", func() {
		_, err := tc.createLocation(s, &v1id, map[string]any{"other": float64(1)})
		r.Error(err)
		r.ErrorContains(err, "jsonschema validation failed")
		r.ErrorContains(err, "missing property 'zone'")
	})

	s.Run("update with data but no dataTypeID", func() {
		loc, err := tc.createLocation(s, &v1id, map[string]any{"zone": "A"})
		r.NoError(err)
		_, err = tc.updateLocation(s, loc.ID, nil, map[string]any{"zone": "B"})
		r.Error(err, "strict-ID contract: updates touching data must re-send the pin")
		r.ErrorContains(err, "data type not set")
	})

	s.Run("pin to a soft-deleted version", func() {
		r.NoError(tc.deleteDT(s, v1.ID))
		// The per-service validator cache drops the row via a NATS event, so
		// rejection is eventually consistent — poll, never sleep.
		r.NoError(tests.PollUntil(s.Ctx, 10*time.Second, 250*time.Millisecond, func() error {
			_, err := tc.createLocation(s, &v1id, map[string]any{"zone": "A"})
			if err == nil {
				return fmt.Errorf("create pinned to a deleted data type still succeeds")
			}
			return nil
		}), "pin to a deleted version must be rejected")
	})
}

// TestCrossTenantPinRejected: a dataTypeID minted in tenant A is invisible
// to tenant B — pinning it from B fails as not-found, so entities can never
// reference another tenant's schema versions.
func (s *VersioningSuite) TestCrossTenantPinRejected() {
	r := s.Require()
	tcA := s.newTenantClient()
	tcB := s.newTenantClient()

	v1 := tcA.mustCreateDT(s, newDataTypeInput(dvSlug("xpin")))
	foreign := mustUUID(s, v1.ID)

	_, err := tcB.createLocation(s, &foreign, map[string]any{"zone": "A"})
	r.Error(err, "tenant B must not be able to pin tenant A's data type")
	r.ErrorContains(err, "data type not found")
}

// TestFuzzedDataRoundTrip: a spread of generated payloads — unicode,
// numbers, bools, nested objects, arrays — survives the write/read
// round-trip byte-for-value intact under a permissive schema, while a
// strictly-typed schema consistently rejects every wrong-typed value.
func (s *VersioningSuite) TestFuzzedDataRoundTrip() {
	r := s.Require()
	tc := s.newTenantClient()

	open := tc.mustCreateDT(s, newDataTypeInput(dvSlug("fuzz-open")))
	openID := mustUUID(s, open.ID)

	s.Run("permissive schema stores fuzzed payloads intact", func() {
		for i := 0; i < 10; i++ {
			// Floats are quarter-steps, not full-precision randoms: the
			// generated client (gqlgenc clientv2 encodeFloat) marshals request
			// floats with %f — fixed 6 decimals — so anything finer is
			// truncated before it reaches the wire. Raw GraphQL round-trips
			// full float64 precision; the boundary is the Go client, and
			// quarter-steps are binary-exact both through %f and back.
			payload := map[string]any{
				"word":    gofakeit.Word(),
				"name":    gofakeit.Name(),
				"unicode": "héllo wörld 🚀 日本語 ¿n?-" + gofakeit.LetterN(4),
				"number":  float64(gofakeit.Number(-1_000_000, 1_000_000)) + 0.25,
				"flag":    gofakeit.Bool(),
				"nested": map[string]any{
					"city": gofakeit.City(),
					"deep": map[string]any{"n": float64(i)},
				},
				"list": []any{gofakeit.Word(), float64(i), gofakeit.Bool()},
			}
			loc, err := tc.createLocation(s, &openID, payload)
			r.NoError(err, "fuzz payload %d rejected", i)
			r.Equal(payload, loc.Data, "fuzz payload %d did not round-trip intact", i)
		}
	})

	s.Run("typed schema rejects wrong-typed values", func() {
		typedIn := newDataTypeInput(dvSlug("fuzz-typed"))
		typedIn.JSONSchema = `{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"],"additionalProperties":false}`
		typed := tc.mustCreateDT(s, typedIn)
		typedID := mustUUID(s, typed.ID)

		good, err := tc.createLocation(s, &typedID, map[string]any{"count": float64(3)})
		r.NoError(err, "integral count must pass")
		r.NotNil(good.DataTypeID)

		for _, bad := range []any{
			gofakeit.Word(),                 // string where integer expected
			gofakeit.Bool(),                 // bool
			1.5,                             // non-integral number
			map[string]any{"n": float64(1)}, // object
			[]any{float64(1)},               // array
		} {
			_, err := tc.createLocation(s, &typedID, map[string]any{"count": bad})
			r.Error(err, "count=%v (%T) must be rejected", bad, bad)
			r.ErrorContains(err, "jsonschema validation failed")
		}
	})
}
