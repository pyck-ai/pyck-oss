package resolvers

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/management/model"
)

// validRegistration is a registerTenant input that passes every check, so
// each case below can spoil exactly one field and the failure names the
// rule that caught it.
func validRegistration() model.RegisterTenantInput {
	return model.RegisterTenantInput{
		Name:           "Acme",
		AdminUsername:  "acme-admin",
		AdminEmail:     "admin@acme.test",
		AdminFirstName: "Ada",
		AdminLastName:  "Lovelace",
		AdminPassword:  "Str0ng!pass",
	}
}

// =============================================================================
// isValidPassword
// =============================================================================

// The upper bound is bcrypt's own and is counted in bytes: a password of 73
// bytes is refused here rather than at the hash, where the library returns
// an error the caller cannot explain. The boundary is exact on purpose:
// off by one in either direction either rejects a password bcrypt accepts
// or accepts one it refuses.
func TestIsValidPassword_ByteBoundaries(t *testing.T) {
	t.Parallel()

	// "Aa1!" satisfies every character class; the filler carries the length.
	pw := func(bytes int) string {
		require.GreaterOrEqual(t, bytes, 4)
		return "Aa1!" + strings.Repeat("x", bytes-4)
	}

	cases := []struct {
		name     string
		password string
		want     bool
	}{
		{"one byte under the minimum", pw(7), false},
		{"exactly the minimum", pw(8), true},
		{"one byte over the minimum", pw(9), true},
		{"the old 70-byte cap is no longer a limit", pw(71), true},
		{"exactly bcrypt's 72-byte cap", pw(72), true},
		{"one byte past bcrypt's cap", pw(73), false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isValidPassword(tc.password),
				"password of %d bytes", len(tc.password))
		})
	}
}

// A multi-byte password is bounded in bytes, not runes: bcrypt counts
// bytes, so a 72-rune password of two-byte runes must be refused even
// though it "looks" short enough.
func TestIsValidPassword_CountsBytesNotRunes(t *testing.T) {
	t.Parallel()

	// 4 ASCII + 35 two-byte runes = 74 bytes but only 39 runes.
	password := "Aa1!" + strings.Repeat("é", 35)
	require.Len(t, []rune(password), 39)
	require.Greater(t, len(password), maxPasswordBytes)

	assert.False(t, isValidPassword(password),
		"the bound is bytes: bcrypt would refuse this input outright")
}

// Every character class is required. Each case drops exactly one of them
// from an otherwise valid password, so a rule quietly deleted from the
// conjunction fails here.
func TestIsValidPassword_RequiresEveryCharacterClass(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		password string
		want     bool
	}{
		{"all four classes", "Aa1!xxxx", true},
		{"no uppercase", "aa1!xxxx", false},
		{"no lowercase", "AA1!XXXX", false},
		{"no digit", "Aab!xxxx", false},
		{"no special character", "Aa1bxxxx", false},
		{"underscore counts as special", "Aa1_xxxx", true},
		{"space counts as special", "Aa1 xxxx", true},
		// \W is ASCII-only in Go, so any non-ASCII rune satisfies the
		// special class, worth pinning, because it is not obvious.
		{"a non-ASCII letter counts as special", "Aa1éxxxx", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isValidPassword(tc.password))
		})
	}
}

// =============================================================================
// validateFutureExpiry / validateRegistration
// =============================================================================

// A registration carrying an expiry that is already past produces a tenant
// the next expiry sweep disables again: provisioned, billed for, and gone
// within the sweep interval. It must be refused at the resolver, before the
// row exists.
func TestValidateFutureExpiry(t *testing.T) {
	t.Parallel()

	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	epoch := time.Unix(0, 0).UTC()

	cases := []struct {
		name      string
		expiresAt *time.Time
		wantErr   bool
	}{
		{"nil means no expiry", nil, false},
		{"an hour in the future", &future, false},
		{"an hour in the past", &past, true},
		{"the zero time", &time.Time{}, true},
		{"the unix epoch", &epoch, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateFutureExpiry(tc.expiresAt)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errExpiryNotFuture)
		})
	}
}

// "Now" is not in the future: a timestamp that has already passed by the
// time the row is written must not slip through the boundary.
func TestValidateFutureExpiry_NowIsNotInTheFuture(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	require.ErrorIs(t, validateFutureExpiry(&now), errExpiryNotFuture)
}

// validateRegistration must carry the expiry rule too, or registerTenant
// would be the one door through which a past expiry reaches the column.
func TestValidateRegistration_RejectsAPastExpiry(t *testing.T) {
	t.Parallel()

	past := time.Now().UTC().Add(-time.Minute)
	future := time.Now().UTC().Add(time.Minute)

	in := validRegistration()
	require.NoError(t, validateRegistration(in), "no expiry at all is accepted")

	in.ExpiresAt = &future
	require.NoError(t, validateRegistration(in))

	in.ExpiresAt = &past
	require.ErrorIs(t, validateRegistration(in), errExpiryNotFuture)
}

// Every field Zitadel would reject at user import (after the org exists
// and holds the name) is refused here instead, so a typo cannot leave an
// error row behind an ACTIVE org.
func TestValidateRegistration_FieldRules(t *testing.T) {
	t.Parallel()

	tooLong := strings.Repeat("x", maxRegistrationFieldRunes+1)
	atLimit := strings.Repeat("x", maxRegistrationFieldRunes)

	cases := []struct {
		name    string
		spoil   func(*model.RegisterTenantInput)
		wantErr bool
	}{
		{"valid", func(*model.RegisterTenantInput) {}, false},
		{"empty name", func(in *model.RegisterTenantInput) { in.Name = "" }, true},
		{"empty username", func(in *model.RegisterTenantInput) { in.AdminUsername = "" }, true},
		{"empty first name", func(in *model.RegisterTenantInput) { in.AdminFirstName = "" }, true},
		{"empty last name", func(in *model.RegisterTenantInput) { in.AdminLastName = "" }, true},
		{"empty e-mail", func(in *model.RegisterTenantInput) { in.AdminEmail = "" }, true},
		{"name at the 200-rune limit", func(in *model.RegisterTenantInput) { in.Name = atLimit }, false},
		{"name past the limit", func(in *model.RegisterTenantInput) { in.Name = tooLong }, true},
		{"username past the limit", func(in *model.RegisterTenantInput) { in.AdminUsername = tooLong }, true},
		{
			"200 multi-byte runes are within the limit",
			func(in *model.RegisterTenantInput) { in.Name = strings.Repeat("é", maxRegistrationFieldRunes) },
			false,
		},
		{
			"201 multi-byte runes are not",
			func(in *model.RegisterTenantInput) { in.Name = strings.Repeat("é", maxRegistrationFieldRunes+1) },
			true,
		},
		{"e-mail with a display name", func(in *model.RegisterTenantInput) { in.AdminEmail = "Ada <a@b.test>" }, true},
		{"e-mail that is not an address", func(in *model.RegisterTenantInput) { in.AdminEmail = "not-an-address" }, true},
		{"e-mail with a trailing comment", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@b.test (Ada)" }, true},
		// Zitadel's e-mail host rule (protoc-gen-validate email: true),
		// which it applies at user import after the org exists.
		{"e-mail on an IDN host", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@b\u00fccher.test" }, true},
		{"e-mail on an IPv4 literal", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@[127.0.0.1]" }, true},
		{"e-mail on an IPv6 literal", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@[IPv6:::1]" }, true},
		{"e-mail host label with a leading hyphen", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@-b.test" }, true},
		{"e-mail host label with a trailing hyphen", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@b-.test" }, true},
		{"e-mail top-level label with a trailing hyphen", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@b.test-" }, true},
		{"e-mail host with an underscore", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@b_c.test" }, true},
		{"e-mail host with a zero-width space", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@b\u200b.test" }, true},
		{"e-mail host label of 64 bytes", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@" + strings.Repeat("b", 64) + ".test" }, true},
		{"e-mail host label of 63 bytes", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@" + strings.Repeat("b", 63) + ".test" }, false},
		{"e-mail local part of 65 bytes", func(in *model.RegisterTenantInput) { in.AdminEmail = strings.Repeat("a", 65) + "@b.test" }, true},
		{"e-mail local part of 64 bytes", func(in *model.RegisterTenantInput) { in.AdminEmail = strings.Repeat("a", 64) + "@b.test" }, false},
		// The local-part bound counts bytes: 33 two-byte runes are 66.
		{"e-mail local part of 33 two-byte runes", func(in *model.RegisterTenantInput) { in.AdminEmail = strings.Repeat("\u00e9", 33) + "@b.test" }, true},
		{"e-mail local part of 32 two-byte runes", func(in *model.RegisterTenantInput) { in.AdminEmail = strings.Repeat("\u00e9", 32) + "@b.test" }, false},
		{"e-mail on an upper-case host", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@EXAMPLE.COM" }, false},
		{"e-mail on a single-label host", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@localhost" }, false},
		{"e-mail on a punycode host", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@xn--bcher-kva.example" }, false},
		{"e-mail on a dotted-quad host", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@1.2.3.4" }, false},
		{"e-mail on a host with digits", func(in *model.RegisterTenantInput) { in.AdminEmail = "admin@w3c0.example9" }, false},
		{"e-mail host with an inner hyphen", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@b-c.test" }, false},
		{"e-mail with a plus tag", func(in *model.RegisterTenantInput) { in.AdminEmail = "a+tag@b.test" }, false},
		// Zitadel lower-cases before it checks the labels, and the Kelvin
		// sign lower-cases to an ASCII k: Zitadel accepts it, so pyck does.
		{"e-mail host with the Kelvin sign", func(in *model.RegisterTenantInput) { in.AdminEmail = "a@\u212aelvin.test" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := validRegistration()
			tc.spoil(&in)

			err := validateRegistration(in)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errRegistrationInvalid)
		})
	}
}

// registrationFields addresses every string field of RegisterTenantInput by
// the name validateRegistration reports it under.
var registrationFields = []struct {
	name string
	set  func(*model.RegisterTenantInput, string)
	get  func(model.RegisterTenantInput) string
}{
	{"name", func(in *model.RegisterTenantInput, v string) { in.Name = v }, func(in model.RegisterTenantInput) string { return in.Name }},
	{"adminUsername", func(in *model.RegisterTenantInput, v string) { in.AdminUsername = v }, func(in model.RegisterTenantInput) string { return in.AdminUsername }},
	{"adminFirstName", func(in *model.RegisterTenantInput, v string) { in.AdminFirstName = v }, func(in model.RegisterTenantInput) string { return in.AdminFirstName }},
	{"adminLastName", func(in *model.RegisterTenantInput, v string) { in.AdminLastName = v }, func(in model.RegisterTenantInput) string { return in.AdminLastName }},
	{"adminEmail", func(in *model.RegisterTenantInput, v string) { in.AdminEmail = v }, func(in model.RegisterTenantInput) string { return in.AdminEmail }},
	{"adminPassword", func(in *model.RegisterTenantInput, v string) { in.AdminPassword = v }, func(in model.RegisterTenantInput) string { return in.AdminPassword }},
}

// Zitadel trims the org name and the user's names before it stores them,
// and Postgres and Zitadel both refuse control characters after the row
// exists. Every string field of the registration is therefore refused when
// it has surrounding whitespace (Unicode spaces included), is blank, or
// carries a control rune, each with the rule it broke and the field it
// broke it in, and the password never echoed.
func TestValidateRegistration_RefusesUntrimmedBlankAndControlInEveryField(t *testing.T) {
	t.Parallel()

	spoilers := []struct {
		name  string
		spoil func(valid string) string
		want  string
	}{
		{"leading space", func(v string) string { return " " + v }, "has leading or trailing whitespace"},
		{"trailing space", func(v string) string { return v + " " }, "has leading or trailing whitespace"},
		{"leading NBSP", func(v string) string { return "\u00a0" + v }, "has leading or trailing whitespace"},
		{"trailing em space", func(v string) string { return v + "\u2003" }, "has leading or trailing whitespace"},
		{"trailing ideographic space", func(v string) string { return v + "\u3000" }, "has leading or trailing whitespace"},
		{"trailing newline", func(v string) string { return v + "\n" }, "has leading or trailing whitespace"},
		{"trailing NEL", func(v string) string { return v + "\u0085" }, "has leading or trailing whitespace"},
		{"empty", func(string) string { return "" }, "is blank"},
		{"spaces only", func(string) string { return "   " }, "is blank"},
		{"NBSP only", func(string) string { return "\u00a0\u00a0" }, "is blank"},
		{"tab only", func(string) string { return "\t" }, "is blank"},
		{"inner NUL", func(v string) string { return v[:1] + "\x00" + v[1:] }, "contains a control character"},
		{"inner tab", func(v string) string { return v[:1] + "\t" + v[1:] }, "contains a control character"},
		{"inner DEL", func(v string) string { return v[:1] + "\x7f" + v[1:] }, "contains a control character"},
		{"inner CR LF", func(v string) string { return v[:1] + "\r\n" + v[1:] }, "contains a control character"},
		{"inner C1 control", func(v string) string { return v[:1] + "\u009b" + v[1:] }, "contains a control character"},
	}
	for _, field := range registrationFields {
		for _, sp := range spoilers {
			t.Run(field.name+"/"+sp.name, func(t *testing.T) {
				t.Parallel()
				in := validRegistration()
				spoiled := sp.spoil(field.get(in))
				field.set(&in, spoiled)

				err := validateRegistration(in)

				if field.name == "adminPassword" {
					// Only the control-rune rule holds for the password:
					// Zitadel stores its hash and trims nothing, and a
					// blank one is isValidPassword's refusal.
					if !strings.ContainsFunc(spoiled, unicode.IsControl) {
						require.NoError(t, err)
						return
					}
					require.ErrorIs(t, err, errRegistrationInvalid)
					assert.Contains(t, err.Error(), "adminPassword contains a control character")
					assert.NotContains(t, err.Error(), validRegistration().AdminPassword, "the password is never echoed")
					return
				}
				require.ErrorIs(t, err, errRegistrationInvalid)
				assert.Contains(t, err.Error(), field.name+" "+sp.want)
			})
		}
	}
}

// The whitespace and control rules are about the edges and about control
// runes, not about any character a real name carries: letters outside
// ASCII, inner spaces of any width and format runes such as a zero-width
// space all still register.
func TestValidateRegistration_AcceptsInnerSpacesAndNonASCIILetters(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		edit func(*model.RegisterTenantInput)
	}{
		{"umlauts in name and names", func(in *model.RegisterTenantInput) {
			in.Name, in.AdminFirstName, in.AdminLastName = "Ärzte GmbH", "Jörg", "Müller"
		}},
		{"inner double space", func(in *model.RegisterTenantInput) { in.Name = "Acme  Corp" }},
		{"inner NBSP", func(in *model.RegisterTenantInput) { in.AdminLastName = "van\u00a0Rossum" }},
		{"inner zero-width space", func(in *model.RegisterTenantInput) { in.Name = "Acme\u200bCorp" }},
		{"circled letters", func(in *model.RegisterTenantInput) { in.Name = "Ⓐcme" }},
		{"inner space in the password", func(in *model.RegisterTenantInput) { in.AdminPassword = "Str0ng! pass" }},
		// The password is hashed as sent and Zitadel trims nothing of it:
		// spaces at its ends are part of it.
		{"trailing space in the password", func(in *model.RegisterTenantInput) { in.AdminPassword = "Str0ng!pass " }},
		{"leading space in the password", func(in *model.RegisterTenantInput) { in.AdminPassword = " Str0ng!pass" }},
		{"leading NBSP in the password", func(in *model.RegisterTenantInput) { in.AdminPassword = "\u00a0Str0ng!pass" }},
		{"trailing ideographic space in the password", func(in *model.RegisterTenantInput) { in.AdminPassword = "Str0ng!pass\u3000" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := validRegistration()
			tc.edit(&in)
			require.NoError(t, validateRegistration(in))
		})
	}
}

// =============================================================================
// validateTenantData
// =============================================================================

// The data map travels into one SetOrgMetadata call, where every value is a
// string: a nested or null value has no representation there, and a run
// that discovers that fails deterministically with an org already created.
// Each case carries exactly one offending key so the assertion cannot
// depend on map iteration order.
func TestValidateTenantData(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		data    map[string]any
		wantErr bool
	}{
		{"nil map", nil, false},
		{"empty map", map[string]any{}, false},
		{"flat scalars survive", map[string]any{
			"flavour": "pyck-go",
			"seats":   float64(12),
			"trial":   true,
		}, false},
		{"a large float is a scalar", map[string]any{"seats": float64(1_000_000)}, false},
		{"a bool is a scalar", map[string]any{"trial": false}, false},
		{"a nested map is not a scalar", map[string]any{"plan": map[string]any{"tier": "gold"}}, true},
		{"a slice is not a scalar", map[string]any{"regions": []any{"eu", "us"}}, true},
		{"a null value has no metadata representation", map[string]any{"plan": nil}, true},
		{"an empty string is rejected by Zitadel", map[string]any{"plan": ""}, true},
		{"an empty key is rejected", map[string]any{"": "value"}, true},
		// Postgres refuses \u0000 in a jsonb column; the insert would fail
		// with driver text after the bcrypt hash.
		{"a NUL in a value has no jsonb representation", map[string]any{"k": "a\x00b"}, true},
		{"a NUL in a key has no jsonb representation", map[string]any{"k\x00": "v"}, true},
		{"other control characters are storable", map[string]any{"address": "line 1\nline 2\t\x01"}, false},
		{"a key at the 200-byte limit survives", map[string]any{
			strings.Repeat("k", maxTenantDataKeyBytes): "value",
		}, false},
		{"a key past the 200-byte limit", map[string]any{
			strings.Repeat("k", maxTenantDataKeyBytes+1): "value",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateTenantData(tc.data)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errTenantDataInvalid)
		})
	}
}

// The key count is bounded exactly: 32 keys are written, 33 are refused.
func TestValidateTenantData_KeyCountBoundary(t *testing.T) {
	t.Parallel()

	build := func(n int) map[string]any {
		data := make(map[string]any, n)
		for i := range n {
			data[string(rune('a'+i%26))+strings.Repeat("k", i/26+1)] = "v"
		}
		require.Len(t, data, n)
		return data
	}

	require.NoError(t, validateTenantData(build(maxTenantDataKeys)))
	require.ErrorIs(t, validateTenantData(build(maxTenantDataKeys+1)), errTenantDataInvalid)
}

// The serialized size is bounded too: the map rides in the outbox payload
// and in workflow history, both of which have their own ceilings.
func TestValidateTenantData_SerializedSizeBoundary(t *testing.T) {
	t.Parallel()

	fits := map[string]any{"blob": strings.Repeat("x", maxTenantDataBytes-64)}
	require.NoError(t, validateTenantData(fits))

	tooBig := map[string]any{"blob": strings.Repeat("x", maxTenantDataBytes)}
	require.ErrorIs(t, validateTenantData(tooBig), errTenantDataInvalid)
}

// A value json.Marshal cannot encode is reported as an encoding failure,
// not as a validation refusal: the two mean different things to the caller,
// and mislabelling one as the other hides a bug in the caller's payload.
func TestValidateTenantData_UnencodableValueIsNotAValidationRefusal(t *testing.T) {
	t.Parallel()

	err := validateTenantData(map[string]any{"ratio": math.Inf(1)})

	require.Error(t, err)
	require.NotErrorIs(t, err, errTenantDataInvalid)
	assert.Contains(t, err.Error(), "encode tenant data")
}

// A GraphQL request carries a value two ways, and the two decode
// differently. gqlgen's HTTP transport decodes the request body (POST and
// GET alike) with a json.Decoder that has UseNumber set, so a number in a
// *variable* reaches the resolver as a json.Number holding the caller's
// own text; gqlparser coerces an *inline literal* to int64 (IntValue) or
// float64 (FloatValue). graphql.UnmarshalMap hands the Map scalar through
// untouched either way.
//
// So the type switch in validateTenantDataValue is a statement about both
// alphabets, and a rule written for one of them is half a rule. This table
// pins the whole alphabet, per transport, at the boundary: what each one
// can produce, and what the gate must answer for it.
func TestValidateTenantData_AcceptsWhatEachTransportProduces(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   any
		wantErr bool
	}{
		// inline literal, coerced by gqlparser
		{"inline integer literal (int64)", int64(12), false},
		{"inline negative integer literal", int64(-1), false},
		{"inline MaxInt64 literal", int64(math.MaxInt64), false},
		{"inline MinInt64 literal", int64(math.MinInt64), false},
		{"inline float literal", 12.5, false},
		{"inline boolean literal", true, false},
		{"inline string literal", "pyck-go", false},

		// variable, decoded with UseNumber
		{"variable number", json.Number("12"), false},
		{"variable number in exponent form", json.Number("1e6"), false},
		{"variable number at the float64 ceiling", json.Number("1e308"), false},
		{"variable number past the float64 ceiling", json.Number("1e400"), true},
		{"variable negative number past the ceiling", json.Number("-1e400"), true},
		{"variable number of 400 digits", json.Number("1" + strings.Repeat("0", 400)), true},
		{"variable number that underflows to zero", json.Number("1e-400"), false},

		// shapes neither transport should get past the gate
		{"a type no decoder produces", struct{ A int }{A: 1}, true},
		{"a pointer", new(int), true},
		{"an untyped int a Go caller might pass", 12, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateTenantData(map[string]any{"k": tc.value})
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errTenantDataInvalid)
		})
	}
}

// Whatever the gate accepts has to survive the row: it is marshalled into
// the data column, and every reader of that row (ent's own scanner, the
// outbox payload the tenant-lifecycle trigger decodes, the registration
// run's LoadTenantRegistration step) decodes it with a plain
// encoding/json into map[string]any.
//
// A value that writes but does not read back is worse than a refusal.
// registerTenant is anonymous and the row is committed before anything
// else happens, so such a row holds the tenant name (names stay reserved
// across every row until a hard delete) while its create event cannot be decoded (no registration
// run ever starts) and deleteTenant cannot read it to retire it. That is
// finding F1: {"a":1e400} is a valid JSON number, a valid GraphQL
// variable, and was accepted.
func TestValidateTenantData_AcceptedValuesSurviveTheRow(t *testing.T) {
	t.Parallel()

	accepted := map[string]any{
		"string":   "pyck-go",
		"bool":     true,
		"int64":    int64(7),
		"float64":  1e21,
		"number":   json.Number("1e6"),
		"ceiling":  json.Number("1e308"),
		"tiny":     json.Number("1e-400"),
		"negative": json.Number("-12.5"),
	}
	require.NoError(t, validateTenantData(accepted))

	stored, err := json.Marshal(accepted)
	require.NoError(t, err)

	var loaded map[string]any
	require.NoError(t, json.Unmarshal(stored, &loaded),
		"the committed row must be readable: %s", stored)
	require.Len(t, loaded, len(accepted))
	require.NoError(t, validateTenantData(loaded),
		"a map accepted before the row was written must still be accepted after reading it back")
}

// The e-mail host rule, checked directly for the bounds the 200-character
// field limit and mail.ParseAddress keep from validateRegistration: the
// whole address of 254 bytes, empty labels and the one trailing dot
// Zitadel drops. Each refusal names the rule it broke.
func TestEmailHostRefusal_Bounds(t *testing.T) {
	t.Parallel()

	label := func(n int, c string) string { return strings.Repeat(c, n) }
	// host253 is three 63-byte labels and a 61-byte one: 253 bytes.
	host253 := label(63, "a") + "." + label(63, "b") + "." + label(63, "c") + "." + label(61, "d")
	require.Len(t, host253, 253)
	// host189 makes a 254-byte address with a 64-byte local part.
	host189 := label(63, "a") + "." + label(63, "b") + "." + label(61, "c")
	local64 := label(maxEmailLocalBytes, "x")

	cases := []struct {
		name, addr, want string
	}{
		// The 254-byte bound on the whole address leaves at most 252 bytes
		// for the host, so Zitadel's own 253-byte host bound never decides:
		// a 253-byte host is refused for the address's length.
		{"host of 252 bytes", "a@" + host253[:252], ""},
		{"host of 253 bytes", "a@" + host253, "address has more than 254 bytes"},
		// The bound is on the address as sent: a trailing dot counts.
		{"host of 251 bytes and a trailing dot", "a@" + host253[:251] + ".", ""},
		{"host of 252 bytes and a trailing dot", "a@" + host253[:252] + ".", "address has more than 254 bytes"},
		{"address of 254 bytes", local64 + "@" + host189, ""},
		{"address of 255 bytes", local64 + "@" + host189 + "c", "address has more than 254 bytes"},
		{"local part of 64 bytes", local64 + "@b.test", ""},
		{"local part of 65 bytes", local64 + "x@b.test", "local part has more than 64 bytes"},
		{"one trailing dot", "a@b.test.", ""},
		{"two trailing dots", "a@b.test..", "host name label must have 1 to 63 bytes"},
		{"an empty inner label", "a@b..test", "host name label must have 1 to 63 bytes"},
		{"an empty host", "a@", "host name label must have 1 to 63 bytes"},
		{"label of 63 bytes", "a@" + label(63, "b") + ".test", ""},
		{"label of 64 bytes", "a@" + label(64, "b") + ".test", "host name label must have 1 to 63 bytes"},
		{"leading hyphen", "a@-b.test", "host name label begins or ends with a hyphen"},
		{"trailing hyphen", "a@b-.test", "host name label begins or ends with a hyphen"},
		{"a lone hyphen", "a@-.test", "host name label begins or ends with a hyphen"},
		{"IDN label", "a@b\u00fccher.test", "host name label holds a character other than an ASCII letter, digit or hyphen"},
		{"IP literal", "a@[127.0.0.1]", "host name label holds a character other than an ASCII letter, digit or hyphen"},
		{"underscore", "a@b_c.test", "host name label holds a character other than an ASCII letter, digit or hyphen"},
		{"upper case", "a@B.TEST", ""},
		// Both ends of the digit range: an off-by-one on either would turn
		// away real hosts Zitadel accepts (web0.example, 365.example).
		{"digits 0 to 9", "a@x0123456789.test", ""},
		// Lower-cased first, as Zitadel does: U+212A becomes "k" (1 byte
		// from 3), so the label lengths are those of the lower-cased host.
		{"Kelvin sign", "a@\u212a.test", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, emailHostRefusal(tc.addr))
		})
	}
}

// The password is exempt from the whitespace and blank rules but not from
// the control-character rule, whichever control rune it is and wherever it
// sits; the refusal names the field and never echoes the password.
func TestValidateRegistration_PasswordRefusesEveryControlRune(t *testing.T) {
	t.Parallel()

	for name, password := range map[string]string{
		"NUL":            "Str0ng!\x00pass",
		"tab":            "Str0ng!\tpass",
		"trailing LF":    "Str0ng!pass\n",
		"CR LF":          "Str0ng!\r\npass",
		"DEL":            "Str0ng!\x7fpass",
		"C1 CSI":         "Str0ng!\u009bpass",
		"leading NEL":    "\u0085Str0ng!pass",
		"escape":         "Str0ng!\x1bpass",
		"only a control": "\x00",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := validRegistration()
			in.AdminPassword = password

			err := validateRegistration(in)

			require.ErrorIs(t, err, errRegistrationInvalid)
			assert.Contains(t, err.Error(), "adminPassword contains a control character")
			assert.NotContains(t, err.Error(), "Str0ng", "the password is never echoed")
		})
	}
}
