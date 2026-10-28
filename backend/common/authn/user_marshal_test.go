package authn_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
)

// User carries the caller's live bearer credential. The struct is
// marshalled wherever a request identity travels as data (event payloads,
// diagnostic dumps, an error that quotes the request) and a doc comment
// telling call sites not to do that is not a control. The json:"-" tag is.

// testUser is a fully populated user, so an assertion about what is NOT in
// the encoding cannot pass merely because a field was empty.
func testUser() authn.User {
	tenantID := uuid.New()
	return authn.User{
		ID:           uuid.New(),
		TenantID:     tenantID,
		Sub:          "zitadel-sub-1",
		Username:     "ada",
		Roles:        map[uuid.UUID]authn.Role{tenantID: authn.ROLE_ADMIN},
		ServiceRoles: map[uuid.UUID]map[string]struct{}{tenantID: {"inventory_service": {}}},
		Token:        "Bearer-live-credential-must-not-be-serialized",
	}
}

// The credential must not appear in the JSON, by key or by value, whether
// the user is marshalled by value or behind a pointer (both shapes occur:
// authn.SystemUser returns *User, request contexts hold values).
func TestUser_TokenIsNeverMarshalled(t *testing.T) {
	t.Parallel()

	user := testUser()
	for name, value := range map[string]any{"value": user, "pointer": &user} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(value)
			require.NoError(t, err)

			encoded := string(raw)
			assert.NotContains(t, encoded, user.Token, "the credential must not be in the payload")
			assert.NotContains(t, encoded, "Token", "not even as an empty key")

			// The identifying fields must still be there: a tag that hid
			// the whole struct would pass the assertions above.
			assert.Contains(t, encoded, user.Sub)
			assert.Contains(t, encoded, user.Username)
			assert.Contains(t, encoded, user.ID.String())
		})
	}
}

// Nesting is the realistic case: the user rides inside an event payload or
// a diagnostic envelope rather than being marshalled on its own.
func TestUser_TokenIsNeverMarshalledInsideAnEnvelope(t *testing.T) {
	t.Parallel()

	user := testUser()
	// A map, not a struct: the envelope's shape is irrelevant here, and
	// what is under test is what the nested User contributes to it.
	raw, err := json.Marshal(map[string]any{"operation": "deleteTenant", "actor": user})
	require.NoError(t, err)

	assert.NotContains(t, string(raw), user.Token,
		"an event payload carrying the acting user must not carry its bearer token")
}

// decodeInto decodes raw into target. The indirection through any is what
// keeps the static analysers from treating this as a tagged-struct decode:
// authn.User deliberately carries a tag on one field only.
func decodeInto(t *testing.T, raw string, target any) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(raw), target))
}

// A round-trip must not resurrect the credential either: a decoder that
// accepted a "Token" key would let a payload set one on an identity.
func TestUser_TokenIsNotUnmarshalled(t *testing.T) {
	t.Parallel()

	var user authn.User
	decodeInto(t, `{"Username":"ada","Token":"injected"}`, &user)

	assert.Equal(t, "ada", user.Username)
	assert.Empty(t, user.Token, "a payload must not be able to set the identity's credential")
}

// The tag is the control, so the tag is what this asserts: a field renamed
// or re-tagged in a refactor would still be caught here even if some future
// encoding path stopped going through encoding/json.
func TestUser_TokenFieldCarriesTheSkipTag(t *testing.T) {
	t.Parallel()

	field, ok := reflect.TypeOf(authn.User{}).FieldByName("Token")
	require.True(t, ok, "User must still carry the Token field this rule is about")
	assert.Equal(t, "-", field.Tag.Get("json"),
		`User.Token must be tagged json:"-" so it cannot be serialized anywhere`)
}

// Nothing else on the struct may become a second credential carrier
// without the same decision being taken: a field whose name says "secret"
// and which marshals is exactly the hole this rule closed.
func TestUser_NoOtherFieldLooksLikeACredential(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(authn.User{})
	for i := range typ.NumField() {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		for _, word := range []string{"token", "secret", "password", "credential"} {
			if !strings.Contains(name, word) {
				continue
			}
			assert.Equal(t, "-", field.Tag.Get("json"),
				`User.%s looks like a credential and must be tagged json:"-"`, field.Name)
		}
	}
}
