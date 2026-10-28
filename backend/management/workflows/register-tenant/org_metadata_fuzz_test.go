package registertenant_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// decodeBothWays returns the two decodings of the same request body that
// reach this activity in production. A plain decode is what every reader
// of the tenants row performs (ent's scanner, the outbox payload, the
// Temporal payload codec), and yields float64 for a number. A decode with
// UseNumber is what gqlgen's HTTP transport performs on the request body
// itself (graphql/handler/transport/http_get.go:jsonDecode, used by
// http_post.go too), and yields a json.Number holding the caller's own
// spelling, exponent included.
//
// Both are fuzzed, because the branch that handles the second is exactly
// the branch a test written against the first never reaches, and it is the
// branch that can carry an exponent into the metadata.
func decodeBothWays(raw string) []map[string]any {
	var out []map[string]any

	var plain map[string]any
	if err := json.Unmarshal([]byte(raw), &plain); err == nil {
		out = append(out, plain)
	}

	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var numbered map[string]any
	if err := dec.Decode(&numbered); err == nil {
		out = append(out, numbered)
	}
	return out
}

// FuzzSetOrgMetadataActivity guards the rendering half of the tenant-data
// contract: the map a caller sent to registerTenant reaches this activity
// after a round-trip through the tenant row's JSON column and Temporal's
// payload codec, and whatever it renders is the text every reader of the
// tenant's org metadata gets back forever. The bug class is the one the
// explicit switch replaced: a %v that writes a float64 in Go's shortest
// form, so a seat count of a million is stored as "1e+06" and read back as
// a word.
//
// The rendering is also a refusal point: the activity runs after the org
// exists, and a value it cannot render fails the run non-retryably, which
// leaves an error row holding the tenant name. So an accepted value must
// render, and a rendered number must still be the same number.
//
// Run continuously with:
//
//	go test ./backend/management/workflows/register-tenant/ -run '^$' -fuzz FuzzSetOrgMetadataActivity
//
// The package links ent, the Temporal SDK and the Zitadel clients, so this
// target executes in the low thousands per second rather than the hundreds
// of thousands a pure parser reaches; the input is bounded accordingly.
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzSetOrgMetadataActivity(f *testing.F) {
	seeds := []string{
		// real tenant data
		`{"flavour":"pyck-go"}`,
		`{"seats":12}`,
		`{"trial":true}`,
		// the number shapes the switch exists for
		`{"seats":1000000}`,
		`{"a":1e6}`, `{"a":1e21}`, `{"a":1e-7}`, `{"a":1e308}`, `{"a":5e-324}`,
		`{"a":-0}`, `{"a":0.1}`, `{"a":-12.25}`,
		`{"a":12345678901234567890}`,
		`{"a":0.30000000000000004}`,
		// strings that look like other types
		`{"a":"1e6"}`, `{"a":"true"}`, `{"a":"null"}`, `{"a":""}`,
		// encoding hostility in the value and in the key
		`{"a":"x\ty\nz"}`, `{"a":"\u0000"}`, `{"a":"\ud83d\ude42"}`,
		`{"\u200b":"v"}`, `{"a b":"v"}`,
		// magnitudes only the UseNumber decoding survives, and which the
		// resolver refuses for exactly that reason
		`{"a":1e400}`, `{"a":-1e400}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		// Bound the input: the resolver caps the stored map at 16 KiB, and
		// a bigger one only re-measures encoding/json.
		if len(raw) > 4096 {
			return
		}

		for _, data := range decodeBothWays(raw) {
			if len(data) != 1 {
				continue // one key per run keeps a failing seed readable
			}

			for _, value := range data {
				number, isNumber := numberOf(value)
				switch value.(type) {
				case string, float64, bool:
				case json.Number:
					if !isNumber {
						// The resolver refuses a magnitude the row could
						// not be read back as (validateTenantDataValue),
						// so the activity never sees one; asserting a
						// rendering here would assert about input no gate
						// passes.
						continue
					}
				default:
					continue // a null, map or slice the resolver already refused
				}

				got, err := renderOrgMetadata(t, value)
				require.NoError(t, err, "the activity refused %#v, which the resolver accepts", value)

				if !isNumber {
					continue
				}

				// 1. Accepted ⟹ no exponent notation reaches the metadata,
				//    whatever the magnitude and whichever decoder produced
				//    the value. This is the whole point of the switch: the
				//    value is read back as text.
				require.False(t, strings.ContainsAny(got, "eE"),
					"BYPASS: %v (%T) rendered as %q, which is exponent notation, not a number a human reads", value, value, got)

				// 2. Accepted ⟹ the rendering is still the same number: the
				//    shortest form that round-trips, not a truncation.
				back, perr := strconv.ParseFloat(got, 64)
				require.NoError(t, perr, "rendered %q is not a number", got)
				// Delta zero: the property is exact round-trip, not
				// approximation. FormatFloat with precision -1 emits the
				// shortest decimal that parses back to the same bits, so
				// anything but bit equality is a rendering that changed
				// the caller's number.
				require.InDelta(t, number, back, 0, "rendering %v as %q changed its value", number, got)

				// 3. Accepted ⟹ the rendering is bounded: 'f' spells a
				//    subnormal out in full, and the whole map travels in one
				//    SetOrgMetadata call inside a single activity attempt.
				require.LessOrEqual(t, len(got), 512,
					"rendering %v produced %d bytes", number, len(got))
			}
		}
	})
}

// numberOf reports the numeric value behind a decoded tenant-data value,
// whichever decoder produced it. A json.Number whose magnitude float64
// cannot hold is not a number for this purpose: the resolver refuses it
// (validateTenantDataValue) because the row could not be read back, so
// asserting a rendering for it would be asserting about input the gate
// never passes.
func numberOf(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
