package resolvers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"github.com/pyck-ai/pyck/backend/common/request"

	"github.com/pyck-ai/pyck/backend/management/model"
)

const (
	// maxTenantDataKeys, maxTenantDataKeyBytes and maxTenantDataBytes
	// bound the data map of registerTenant. Zitadel caps a metadata key at
	// 200 characters, and the whole map travels in one SetOrgMetadata call
	// inside a single 30 s activity attempt of the registration run, in the
	// outbox payload and in workflow history;
	// a map past these bounds fails the run deterministically and leaves
	// an error row with an org behind.
	maxTenantDataKeys     = 32
	maxTenantDataKeyBytes = 200
	maxTenantDataBytes    = 16 * 1024

	// maxRegistrationFieldRunes is Zitadel's length limit for an org
	// name, a username, a first or last name and an e-mail address. A
	// value past it is rejected at user import, after the org exists.
	maxRegistrationFieldRunes = 200

	// minPasswordBytes and maxPasswordBytes bound the admin password.
	// The upper bound is bcrypt's own: it hashes the first 72 bytes and
	// golang.org/x/crypto/bcrypt refuses longer input outright, so a
	// password past it would fail at the hash rather than at validation.
	minPasswordBytes = 8
	maxPasswordBytes = 72

	// maxEmailBytes, maxEmailLocalBytes and maxHostLabelBytes are the
	// e-mail bounds of Zitadel's request validation (emailHostRefusal),
	// all counted in bytes.
	maxEmailBytes      = 254
	maxEmailLocalBytes = 64
	maxHostLabelBytes  = 63
)

// errRegistrationInvalid is the refusal for a tenant name or admin
// account Zitadel would reject or rewrite at org creation or user import.
var errRegistrationInvalid = errors.New("tenant name and admin account fields must be non-blank, carry no leading or trailing whitespace and no control characters, and at most 200 characters, the password free of control characters and in composed Unicode form (NFC), the e-mail a plain address on an ASCII host name")

// errTenantDataInvalid is the refusal for a data map the registration
// run could not write as org metadata.
var errTenantDataInvalid = errors.New("tenant data must be a flat map of at most 32 non-empty scalar values within 16 KiB, no key or value holding a NUL character")

// errPasswordInvalid is the refusal for an admin password that does not
// meet the rule isValidPassword enforces.
var errPasswordInvalid = errors.New("admin password does not meet the complexity rule")

// errExpiryNotFuture is the refusal for an expiry timestamp that is not in
// the future: the expiry sweep would disable the tenant on its next pass.
var errExpiryNotFuture = errors.New("expiresAt must be in the future")

// errTenantStreamUnavailable answers a restore whose tenant event stream could
// not be created; the cause is logged, not returned.
var errTenantStreamUnavailable = errors.New("the tenant's event stream could not be created; try again")

// errRegistrationFeaturesRefused is the refusal for a registerTenant
// request that carries an X-Pyck-Feature header. The features change how
// the row's own transaction behaves (suppressevents skips the outbox
// write, so no registration run is ever started and the row stays pending
// for good), and registerTenant is open to anonymous callers.
var errRegistrationFeaturesRefused = errors.New("registerTenant accepts no X-Pyck-Feature header")

// errRegistrationUnfinished is the refusal for restoring a tenant whose
// registration never finished: there is nothing behind the row to bring
// back.
var errRegistrationUnfinished = errors.New("tenant registration did not finish")

var (
	passwordUppercaseRegex   = regexp.MustCompile(`[A-Z]`)
	passwordLowercaseRegex   = regexp.MustCompile(`[a-z]`)
	passwordNumberRegex      = regexp.MustCompile(`\d`)
	passwordSpecialCharRegex = regexp.MustCompile(`[\W_]`)
)

// isValidPassword is the admin password rule of registerTenant: one
// uppercase letter, one lowercase letter, one digit and one special
// character, within the byte bounds bcrypt accepts. The bounds are in
// bytes, not runes, because that is what bcrypt counts.
func isValidPassword(password string) bool {
	minMaxLen := len(password) >= minPasswordBytes && len(password) <= maxPasswordBytes
	return passwordUppercaseRegex.MatchString(password) &&
		passwordLowercaseRegex.MatchString(password) &&
		passwordNumberRegex.MatchString(password) &&
		passwordSpecialCharRegex.MatchString(password) &&
		minMaxLen
}

// validateRegistration refuses a tenant name or admin account the
// registration run could not provision as given:
//
//   - a blank value, or one with leading or trailing whitespace (Unicode
//     spaces such as NBSP included): Zitadel trims the org name and the
//     user's names before it stores them, so " Acme" passes the tenant
//     name index next to a live "Acme" and then collides inside Zitadel,
//     and a whitespace-only first name fails user import after the org
//     exists;
//   - a control character (NUL included): Postgres refuses NUL in text
//     and Zitadel refuses control characters, both after the request was
//     accepted;
//   - an over-long value (Zitadel's 200-character limit) or an e-mail that
//     is not a single plain address on a host name Zitadel accepts
//     (emailHostRefusal).
//
// Each of these would otherwise end as an error row, possibly with an
// ACTIVE org holding the name. The password is held to two rules only,
// because Zitadel stores its hash, trims nothing and compares the login's
// bytes with it, never normalizing: no control character, which no login
// form sends, and Unicode NFC (composed) form, which keyboards on every
// platform send: a decomposed password ("o" + U+0308) would register
// bytes the admin cannot type. It is refused, not normalized, so the hash
// is always of the bytes as sent. Surrounding and inner spaces are part of
// the password; isValidPassword bounds it, and it is never echoed in the
// refusal.
func validateRegistration(input model.RegisterTenantInput) error {
	fields := []struct{ name, value string }{
		{"name", input.Name},
		{"adminUsername", input.AdminUsername},
		{"adminFirstName", input.AdminFirstName},
		{"adminLastName", input.AdminLastName},
		{"adminEmail", input.AdminEmail},
	}
	for _, f := range fields {
		trimmed := strings.TrimSpace(f.value)
		switch {
		case trimmed == "":
			return fmt.Errorf("%w: %s is blank", errRegistrationInvalid, f.name)
		case trimmed != f.value:
			return fmt.Errorf("%w: %s has leading or trailing whitespace", errRegistrationInvalid, f.name)
		case strings.ContainsFunc(f.value, unicode.IsControl):
			return fmt.Errorf("%w: %s contains a control character", errRegistrationInvalid, f.name)
		}
		if n := utf8.RuneCountInString(f.value); n > maxRegistrationFieldRunes {
			return fmt.Errorf("%w: %s has %d characters", errRegistrationInvalid, f.name, n)
		}
	}
	if strings.ContainsFunc(input.AdminPassword, unicode.IsControl) {
		return fmt.Errorf("%w: adminPassword contains a control character", errRegistrationInvalid)
	}
	if !norm.NFC.IsNormalString(input.AdminPassword) {
		return fmt.Errorf("%w: adminPassword is not in composed Unicode form (NFC)", errRegistrationInvalid)
	}
	addr, err := mail.ParseAddress(input.AdminEmail)
	if err != nil || addr.Address != input.AdminEmail {
		return fmt.Errorf("%w: adminEmail %q", errRegistrationInvalid, input.AdminEmail)
	}
	if reason := emailHostRefusal(addr.Address); reason != "" {
		return fmt.Errorf("%w: adminEmail %q: %s", errRegistrationInvalid, input.AdminEmail, reason)
	}
	return validateFutureExpiry(input.ExpiresAt)
}

// emailHostRefusal applies the rule Zitadel's request validation puts on
// the admin e-mail at user import (protoc-gen-validate's `email: true`,
// the version Zitadel builds with), transcribed check for check so pyck
// refuses exactly what Zitadel refuses: at most 254 bytes in all, a local
// part of at most 64 bytes, and a host name (after lower-casing and one
// trailing dot dropped) whose dot-separated labels are 1 to 63 bytes of
// ASCII letters, digits and inner hyphens. Zitadel also bounds the host at
// 253 bytes; that check is left out because the 254-byte bound on the
// address, with its '@' and at least one local byte, already caps the host
// at 252, so the host bound can never be what refuses an address. That
// refuses IDN (non-ASCII) labels, IP and domain literals ("[...]") and
// underscores. Zitadel checks it only after the org exists, so an address
// it refuses would otherwise end as an error row with an ACTIVE org
// holding the name. addr is the plain address mail.ParseAddress returned;
// the result is the rule it breaks, or "" when Zitadel accepts it.
// Lower-casing first is Zitadel's order too, so a label whose upper-case
// rune lower-cases to ASCII (the Kelvin sign) passes in both.
func emailHostRefusal(addr string) string {
	if len(addr) > maxEmailBytes {
		return fmt.Sprintf("address has more than %d bytes", maxEmailBytes)
	}
	// mail.ParseAddress guarantees the '@'; without one the empty host
	// would fail the label rule below.
	local, host, _ := strings.Cut(addr, "@")
	if len(local) > maxEmailLocalBytes {
		return fmt.Sprintf("local part has more than %d bytes", maxEmailLocalBytes)
	}
	for _, label := range strings.Split(strings.ToLower(strings.TrimSuffix(host, ".")), ".") {
		if reason := hostLabelRefusal(label); reason != "" {
			return reason
		}
	}
	return ""
}

// hostLabelRefusal is emailHostRefusal's rule for one lower-cased label of
// the host name.
func hostLabelRefusal(label string) string {
	if label == "" || len(label) > maxHostLabelBytes {
		return fmt.Sprintf("host name label must have 1 to %d bytes", maxHostLabelBytes)
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return "host name label begins or ends with a hyphen"
	}
	for _, r := range label {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return "host name label holds a character other than an ASCII letter, digit or hyphen"
		}
	}
	return ""
}

// validateFutureExpiry refuses an expiry timestamp that is not in the
// future. A past value is written to expires_at like any other and the
// next tenant-expiry-check sweep disables the tenant on it, so a
// registration or a restore carrying one produces a tenant that is gone
// again within the sweep interval. A nil timestamp means "no expiry" and
// is accepted.
func validateFutureExpiry(expiresAt *time.Time) error {
	if expiresAt == nil || expiresAt.After(time.Now().UTC()) {
		return nil
	}
	return fmt.Errorf("%w: %s", errExpiryNotFuture, expiresAt.UTC().Format(time.RFC3339))
}

// callerContext scopes ctx to exactly one tenant while keeping the
// caller's identity. Both halves are load-bearing on an audited tenant
// write: HistoryMixin stamps deleted_by/updated_by from the context user
// and overwrites any explicit value, and the outbox hook derives the event
// topic from the single tenant on the context, because Tenant carries no
// tenant_id column of its own.
func callerContext(ctx context.Context, tenantID uuid.UUID) context.Context {
	user := request.ForContext(ctx).User()
	return request.Context(ctx, &user, tenantID)
}

// validateTenantDataValue refuses one tenant-data value the registration
// pipeline could not carry. The accepted set is an allowlist of exactly
// the types the two GraphQL decoders produce, not a denylist of the ones
// seen so far: gqlgen's HTTP transport decodes every request body through
// a json.Decoder with UseNumber, so a number in a *variable* arrives as a
// json.Number holding the caller's own text, while gqlparser coerces an
// inline literal to int64 or float64. A denylist admits all three by
// accident, and json.Number is the one that bites.
//
// json.Number may hold a magnitude float64 has no room for: {"a":1e400}
// is a valid JSON number and a valid GraphQL variable, and the row would
// store that text verbatim. Every reader of the row decodes it into
// map[string]any with a plain encoding/json, which fails for such a
// number, so the committed row holds the tenant name (names stay
// reserved across every row until a hard delete) while the lifecycle
// trigger cannot decode its create event, no registration run ever
// starts, and deleteTenant cannot read the row to retire it either. registerTenant is anonymous, so refusing
// it here is what keeps a name from being taken out of circulation for
// good by one request.
func validateTenantDataValue(key string, value any) error {
	switch v := value.(type) {
	case string:
		if v == "" {
			return fmt.Errorf("%w: %q is empty", errTenantDataInvalid, key)
		}
		if strings.ContainsRune(v, 0) {
			return fmt.Errorf("%w: %q holds a NUL character", errTenantDataInvalid, key)
		}
	case bool, int64, float64:
		// int64 and float64 are what gqlparser coerces an inline literal
		// to; both are finite, because gqlparser refuses a literal that
		// does not fit.
	case json.Number:
		if _, err := v.Float64(); err != nil {
			return fmt.Errorf("%w: %q is not a number the tenant row can be read back as: %s",
				errTenantDataInvalid, key, v.String())
		}
	default:
		return fmt.Errorf("%w: %q is not a scalar", errTenantDataInvalid, key)
	}
	return nil
}

// validateTenantData refuses a data map that the registration run's
// SetOrgMetadataActivity could not write: too many keys, an over-long
// key, a nested or null value (org metadata is a string per key), an
// empty string (Zitadel rejects empty metadata values) or a serialized
// size past maxTenantDataBytes. A nil map is valid.
//
// A NUL character in a key or a string value is refused too: encoding/json
// writes it as the escape \u0000, which Postgres refuses in a jsonb column
// (22P05), so the row insert would fail after the bcrypt hash was paid and
// answer the anonymous caller with driver text.
func validateTenantData(data map[string]any) error {
	if len(data) > maxTenantDataKeys {
		return fmt.Errorf("%w: %d keys", errTenantDataInvalid, len(data))
	}
	for key, value := range data {
		if key == "" || len(key) > maxTenantDataKeyBytes || strings.ContainsRune(key, 0) {
			return fmt.Errorf("%w: key %q", errTenantDataInvalid, key)
		}
		if err := validateTenantDataValue(key, value); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode tenant data: %w", err)
	}
	if len(raw) > maxTenantDataBytes {
		return fmt.Errorf("%w: %d bytes", errTenantDataInvalid, len(raw))
	}
	return nil
}
