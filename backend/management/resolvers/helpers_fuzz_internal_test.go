package resolvers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/unicode/norm"

	"github.com/pyck-ai/pyck/backend/management/ent/types"
	"github.com/pyck-ai/pyck/backend/management/model"
)

// FuzzIsValidPassword guards the validator/sink boundary of the admin
// password: registerTenant answers a password isValidPassword refuses with
// errPasswordInvalid, and hands everything else straight to
// bcrypt.GenerateFromPassword. bcrypt refuses an input longer than 72 bytes
// with an error of its own, so a cap that drifts above bcrypt's turns a
// typed refusal into "hash admin password: ...", a 500-shaped answer to
// what is a caller mistake, on the one mutation an unauthenticated caller
// can reach.
//
// Run continuously with:
//
//	go test ./backend/management/resolvers/ -run '^$' -fuzz FuzzIsValidPassword
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzIsValidPassword(f *testing.F) {
	seeds := []string{
		// real passwords
		"Str0ng!pass", "Aa1!aaaa", "P@ssw0rd123",
		// boundaries: the minimum, and 71, 72 and 73 bytes
		"Aa1!" + strings.Repeat("x", minPasswordBytes-4),
		"Aa1!" + strings.Repeat("x", maxPasswordBytes-5),
		"Aa1!" + strings.Repeat("x", maxPasswordBytes-4),
		"Aa1!" + strings.Repeat("x", maxPasswordBytes-3),
		// one character class missing at a time
		"aa1!aaaa", "AA1!AAAA", "Aa!!aaaa", "Aa11aaaa",
		// multi-byte runes: the cap counts bytes, so these are far fewer
		// characters than the number of bytes suggests
		"Aa1!" + strings.Repeat("\u00e9", 34),
		"Aa1!" + strings.Repeat("\U0001f600", 17),
		// encoding hostility
		"Aa1!\x00\x00\x00\x00", "Aa1!\xff\xfe\xfd\xfc", "Aa1!\u200b\u200b\u200b\u200b",
		"Aa1!\n\r\t x", "", "A", strings.Repeat("Aa1!", 4096),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, password string) {
		if !isValidPassword(password) {
			return // refused input never reaches bcrypt
		}

		// 1. Accepted ⟹ the byte bounds hold. They are bytes, not runes,
		//    because that is what bcrypt counts.
		require.GreaterOrEqual(t, len(password), minPasswordBytes)
		require.LessOrEqual(t, len(password), maxPasswordBytes)

		// 2. Accepted ⟹ bcrypt hashes it. MinCost keeps the fuzzer fast;
		//    the length rule bcrypt enforces does not depend on the cost.
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		require.NoError(t, err, "isValidPassword accepted %q but bcrypt refused it", password)

		// 3. Accepted ⟹ the hash is the whole password's, not a prefix's:
		//    a silently truncated password would let a shorter string open
		//    the tenant's admin account.
		require.NoError(t, bcrypt.CompareHashAndPassword(hash, []byte(password)))
	})
}

// seedPassword is a password isValidPassword accepts, for the seed rows
// that probe the other fields.
const seedPassword = "Str0ng!pass"

// registrationSeeds is FuzzValidateRegistration's seed corpus.
var registrationSeeds = []struct{ name, username, first, last, email, password string }{
	// real registrations
	{"Acme", "acme-admin", "Ada", "Lovelace", "admin@acme.test", seedPassword},
	{"\u00c4rzte GmbH", "arzt", "J\u00f6rg", "M\u00fcller", "joerg@aerzte.test", seedPassword},
	// names whose fold differs between Postgres' libc lower() and Go's
	// strings.ToLower, or is context-sensitive elsewhere (final sigma,
	// dotted capital I), or new in Go 1.27's Unicode 17 tables
	{"\u24b6cme", "a", "A", "L", "a@b.test", seedPassword},
	{"\u0130stanbul", "a", "A", "L", "a@b.test", seedPassword},
	{"\u0391\u03a3", "a", "A", "L", "a@b.test", seedPassword},
	{"\u1c89acme", "a", "A", "L", "a@b.test", seedPassword},
	{"Acme\u200b", "a", "A", "L", "a@b.test", seedPassword},
	// e-mail shapes RFC 5322 allows but a plain address must not take:
	// a display name, an angle-addr, a comment, a quoted local part
	// and a second address all parse, and all differ from the input.
	{"Acme", "a", "A", "L", "Ada <a@b.test>", seedPassword},
	{"Acme", "a", "A", "L", "<a@b.test>", seedPassword},
	{"Acme", "a", "A", "L", "a@b.test (Ada)", seedPassword},
	{"Acme", "a", "A", "L", `"a b"@c.test`, seedPassword},
	{"Acme", "a", "A", "L", `"a@b"@c.test`, seedPassword},
	{"Acme", "a", "A", "L", "a@b.test, c@d.test", seedPassword},
	{"Acme", "a", "A", "L", "a@[127.0.0.1]", seedPassword},
	// header-injection and control-byte attempts
	{"Acme", "a", "A", "L", "a@b.test\r\nBcc: victim@c.test", seedPassword},
	{"Acme", "a", "A", "L", "a@b.test\x00", seedPassword},
	{"Acme\n", "a\n", "A", "L", "a@b.test", seedPassword},
	{"Ac\x00me", "a", "A", "L", "a@b.test", seedPassword},
	{"Acme", "a", "A\tda", "L", "a@b.test", seedPassword},
	{"Acme", "a", "A", "L\x7f", "a@b.test", seedPassword},
	{"Acme", "a", "A", "L", "a@b.test", "Str0ng!\x00pass"},
	// surrounding and whitespace-only values, Unicode spaces included:
	// Zitadel trims these, so they collide with or blank out what pyck
	// checked
	{" Acme", "a", "A", "L", "a@b.test", seedPassword},
	{"Acme\u00a0", "a", "A", "L", "a@b.test", seedPassword},
	{"Acme", "a", "\u2003", "L", "a@b.test", seedPassword},
	{"Acme", "a", "A", "   ", "a@b.test", seedPassword},
	{"Acme", " a", "A", "L", "a@b.test", seedPassword},
	{"Acme", "a", "A", "L", "a@b.test", seedPassword + " "},
	{"\u3000", "a", "A", "L", "a@b.test", seedPassword},
	// zero-width and bidi runes, which parse as an ordinary address
	{"Acme", "a", "A", "L", "a@b\u200b.test", seedPassword},
	{"Acme\u202e", "a", "A", "L", "a@b.test", seedPassword},
	{"Acme\u200b", "a", "A", "L", "a@b.test", seedPassword},
	// bounds
	{strings.Repeat("x", maxRegistrationFieldRunes), "a", "A", "L", "a@b.test", seedPassword},
	{strings.Repeat("x", maxRegistrationFieldRunes+1), "a", "A", "L", "a@b.test", seedPassword},
	{strings.Repeat("\u00e9", maxRegistrationFieldRunes), "a", "A", "L", "a@b.test", seedPassword},
	{"", "", "", "", "", ""},
	{"A", "a", "A", "L", "a@b", seedPassword},
	// e-mail hosts Zitadel's host-name rule refuses at user import: IDN,
	// IP literals, edge hyphens, underscores, over-long labels and local
	// parts, and the ones it accepts: punycode, upper case, a single
	// label, the Kelvin sign that lower-cases to ASCII
	{"Acme", "a", "A", "L", "a@b\u00fccher.test", seedPassword},
	{"Acme", "a", "A", "L", "a@[IPv6:::1]", seedPassword},
	{"Acme", "a", "A", "L", "a@-b.test", seedPassword},
	{"Acme", "a", "A", "L", "a@b-.test", seedPassword},
	{"Acme", "a", "A", "L", "a@b_c.test", seedPassword},
	{"Acme", "a", "A", "L", "a@" + strings.Repeat("b", 63) + ".test", seedPassword},
	{"Acme", "a", "A", "L", "a@" + strings.Repeat("b", 64) + ".test", seedPassword},
	{"Acme", "a", "A", "L", strings.Repeat("a", 64) + "@b.test", seedPassword},
	{"Acme", "a", "A", "L", strings.Repeat("a", 65) + "@b.test", seedPassword},
	{"Acme", "a", "A", "L", strings.Repeat("\u00e9", 33) + "@b.test", seedPassword},
	{"Acme", "a", "A", "L", "a@xn--bcher-kva.example", seedPassword},
	{"Acme", "a", "A", "L", "a@EXAMPLE.COM", seedPassword},
	{"Acme", "a", "A", "L", "a@\u212aelvin.test", seedPassword},
	{"Acme", "a", "A", "L", "a@1.2.3.4", seedPassword},
	// passwords with surrounding and inner spaces, which are part of the
	// password Zitadel stores a hash of
	{"Acme", "a", "A", "L", "a@b.test", " " + seedPassword},
	{"Acme", "a", "A", "L", "a@b.test", "\u00a0" + seedPassword + "\u00a0"},
	{"Acme", "a", "A", "L", "a@b.test", "Str0ng! pass"},
	{"Acme", "a", "A", "L", "a@b.test", seedPassword + "\u0085"},
	// passwords in and out of NFC: a decomposed umlaut ("o" + U+0308) no
	// keyboard sends, its composed form, and a lone combining mark
	{"Acme", "a", "A", "L", "a@b.test", "Str0ng!paso\u0308"},
	{"Acme", "a", "A", "L", "a@b.test", "Str0ng!pas\u00f6"},
	{"Acme", "a", "A", "L", "a@b.test", "\u0308" + seedPassword},
	// invalid UTF-8
	{"\xff", "a", "A", "L", "a@b.test", seedPassword},
	{"Acme", "a", "A", "L", "\xffa@b.test", seedPassword},
}

// FuzzValidateRegistration guards the accepted-but-unprovisionable class:
// every field it lets through is written to Zitadel at org creation and
// user import, which run after the tenant row is committed. A value
// Zitadel refuses there ends the registration as an error row that keeps
// the tenant name out of circulation: nothing is compensated, and the
// name cannot be registered again until an operator removes the row.
//
// Run continuously with:
//
//	go test ./backend/management/resolvers/ -run '^$' -fuzz FuzzValidateRegistration
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzValidateRegistration(f *testing.F) {
	for _, s := range registrationSeeds {
		f.Add(s.name, s.username, s.first, s.last, s.email, s.password)
	}

	f.Fuzz(func(t *testing.T, name, username, first, last, email, password string) {
		// Bound the input: the rule is a rune count, and a megabyte of it
		// measures utf8.RuneCountInString rather than the rule.
		for _, v := range []string{name, username, first, last, email, password} {
			if len(v) > 4096 {
				return
			}
		}

		in := model.RegisterTenantInput{
			Name:           name,
			AdminUsername:  username,
			AdminFirstName: first,
			AdminLastName:  last,
			AdminEmail:     email,
			AdminPassword:  password,
		}
		if err := validateRegistration(in); err != nil {
			return // refused input never reaches Zitadel
		}

		// 1. Accepted ⟹ every field but the password is within Zitadel's
		//    length limit and non-empty. An empty one is refused at user
		//    import, which runs after the org exists.
		for field, value := range map[string]string{
			"name": name, "adminUsername": username,
			"adminFirstName": first, "adminLastName": last, "adminEmail": email,
		} {
			n := utf8.RuneCountInString(value)
			require.Positive(t, n, "%s is empty", field)
			require.LessOrEqual(t, n, maxRegistrationFieldRunes, "%s has %d runes", field, n)
		}

		// 1a. Accepted ⟹ every field but the password is exactly what
		//     Zitadel keeps of it: no surrounding whitespace for it to trim
		//     (Unicode spaces included), not blank, and no control rune for
		//     Postgres or Zitadel to refuse after the row exists. " Acme"
		//     next to a live "Acme" would pass the name index and collide
		//     inside Zitadel; a blank first name fails user import with the
		//     org already created. The password is only hashed (Zitadel
		//     trims nothing of it) so of these only the control-rune rule
		//     holds for it; it must also be NFC, the form keyboards send,
		//     because Zitadel compares the login's bytes with the hash and
		//     never normalizes.
		for field, value := range map[string]string{
			"name": name, "adminUsername": username, "adminFirstName": first,
			"adminLastName": last, "adminEmail": email,
		} {
			require.Equal(t, strings.TrimSpace(value), value, "accepted %s %q has surrounding whitespace", field, value)
			require.NotEmpty(t, strings.TrimSpace(value), "accepted %s is blank", field)
			require.False(t, strings.ContainsFunc(value, unicode.IsControl), "accepted %s %q carries a control rune", field, value)
		}
		require.False(t, strings.ContainsFunc(password, unicode.IsControl), "accepted password carries a control rune")
		require.True(t, norm.NFC.IsNormalString(password), "accepted password %q is not NFC", password)

		// 2. Accepted ⟹ the e-mail is a bare address and nothing else: no
		//    display name, angle-addr, comment or second address survived,
		//    and re-parsing what was accepted yields the same address. A
		//    display name reaching Zitadel would make the admin account's
		//    login differ from the string the caller sent.
		addr, err := mail.ParseAddress(email)
		require.NoError(t, err)
		require.Equal(t, email, addr.Address, "accepted e-mail is not its own address")
		require.Empty(t, addr.Name, "a display name survived validation")
		reparsed, err := mail.ParseAddress(addr.Address)
		require.NoError(t, err, "accepted address does not re-parse")
		require.Equal(t, addr.Address, reparsed.Address, "parsing is not idempotent")

		// 3. Accepted ⟹ the address is valid UTF-8 and carries no control
		//    rune: a CR or LF in an address is the classic header
		//    injection, and Zitadel stores the value as the account's
		//    login. Format runes (zero-width, bidi overrides) are NOT
		//    asserted against in the local part: mail.ParseAddress accepts
		//    them there and so does Zitadel, which only bounds the local
		//    part's length. In the host name they are refused by the
		//    ASCII label rule ("a@b\u200b.test" in the corpus above).
		require.True(t, utf8.ValidString(email), "accepted invalid UTF-8")
		for _, r := range email {
			require.False(t, unicode.IsControl(r), "accepted control rune %U in %q", r, email)
		}

		// 3a. Accepted ⟹ Zitadel's own e-mail rule accepts the address:
		//     protoc-gen-validate's `email: true`, which Zitadel applies to
		//     the admin e-mail at user import, after the org exists. An
		//     address it refuses would end the registration as an error row
		//     with an ACTIVE org holding the name.
		require.NoError(t, pgvEmail(email), "accepted e-mail %q is one Zitadel refuses", email)

		// 4. Accepted ⟹ the name's tenant_name_key value is exactly the
		//    key Zitadel's org-name uniqueness constraint derives from it
		//    (strings.ToLower over the trimmed name), and folding it again
		//    changes nothing. Two accepted names therefore collide on the
		//    index exactly when Zitadel would refuse the second org: a key
		//    that folds less lets a pair through to an error row, one that
		//    folds more refuses a name Zitadel would take.
		zitadelKey := strings.ToLower(strings.TrimSpace(name))
		key := types.TenantNameKey(name)
		require.Equal(t, zitadelKey, key, "name %q: tenant_name_key disagrees with Zitadel's org-name key", name)
		require.Equal(t, key, types.TenantNameKey(key), "name %q: the key is not a fixed point", name)
	})
}

// pgvEmail is protoc-gen-validate's `email: true` check as generated into
// Zitadel's request validators (templates/goshared/known.go, _validateEmail
// and _validateHostname), copied statement for statement as the fuzz
// oracle: the production check is written to the same rule, and the two
// must agree on every input the fuzzer finds.
func pgvEmail(addr string) error {
	a, err := mail.ParseAddress(addr)
	if err != nil {
		return err
	}
	addr = a.Address

	if len(addr) > 254 {
		return errors.New("email addresses cannot exceed 254 characters")
	}

	parts := strings.SplitN(addr, "@", 2)

	if len(parts[0]) > 64 {
		return errors.New("email address local phrase cannot exceed 64 characters")
	}

	host := parts[1]
	s := strings.ToLower(strings.TrimSuffix(host, "."))

	if len(host) > 253 {
		return errors.New("hostname cannot exceed 253 characters")
	}

	for _, part := range strings.Split(s, ".") {
		if l := len(part); l == 0 || l > 63 {
			return errors.New("hostname part must be non-empty and cannot exceed 63 characters")
		}

		if part[0] == '-' {
			return errors.New("hostname parts cannot begin with hyphens")
		}

		if part[len(part)-1] == '-' {
			return errors.New("hostname parts cannot end with hyphens")
		}

		for _, r := range part {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return fmt.Errorf("hostname parts can only contain alphanumeric characters or hyphens, got %q", string(r))
			}
		}
	}

	return nil
}

// FuzzEmailHostRefusalAgreesWithZitadel guards both directions of the
// admin e-mail rule against Zitadel's own (pgvEmail). FuzzValidateRegistration
// only asserts that what pyck accepts Zitadel accepts too; a transcription
// that drifted stricter (a bound off by one, a label rule checked before
// lower-casing) would pass it while refusing addresses Zitadel takes, and
// a caller with such an address could never register. Over every plain
// address (mail.ParseAddress yields the input back, which is the only form
// validateRegistration hands emailHostRefusal), the two must give the same
// verdict.
//
// Run continuously with:
//
//	go test ./backend/management/resolvers/ -run '^$' -fuzz FuzzEmailHostRefusalAgreesWithZitadel
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzEmailHostRefusalAgreesWithZitadel(f *testing.F) {
	label := func(n int, c string) string { return strings.Repeat(c, n) }
	for _, s := range []struct{ local, host string }{
		// real addresses
		{"admin", "acme.test"},
		{"joerg", "aerzte.test"},
		{"a", "b"},
		{"a", "1.2.3.4"},
		{"a", "0123456789.test"},
		{"A", "ADV.TEST"},
		{"!#$%&'*+/=?^_`{|}~-", "adv.test"},
		{"\u00fcser", "adv.test"},
		{"a\u200bb", "adv.test"},
		// the bounds: label 63/64, local 64/65, address 254/255, host 252/253
		{"a", label(63, "b") + ".test"},
		{"a", label(64, "b") + ".test"},
		{label(64, "a"), "b.test"},
		{label(65, "a"), "b.test"},
		{label(64, "x"), label(63, "a") + "." + label(63, "b") + "." + label(61, "c")},
		{label(64, "x"), label(63, "a") + "." + label(63, "b") + "." + label(62, "c")},
		{"a", label(63, "a") + "." + label(63, "b") + "." + label(63, "c") + "." + label(60, "d")},
		{"a", label(63, "a") + "." + label(63, "b") + "." + label(63, "c") + "." + label(61, "d")},
		// multi-byte runes on either side of the byte bounds; the Kelvin
		// sign is 3 bytes that lower-case to the 1-byte "k"
		{label(32, "\u00e9"), "b.test"},
		{label(33, "\u00e9"), "b.test"},
		{"a", label(21, "\u212a") + ".test"},
		{"a", label(22, "\u212a") + ".test"},
		{"a", label(84, "\u212a") + "." + label(84, "\u212a")},
		// hosts Zitadel refuses
		{"a", "b\u00fccher.test"},
		{"a", "[127.0.0.1]"},
		{"a", "[IPv6:::1]"},
		{"a", "-b.test"},
		{"a", "b-.test"},
		{"a", "b_c.test"},
		{"a", "-.test"},
		{"a", "\u0130.test"},
		{"a", "\u017f.test"},
		// shapes mail.ParseAddress refuses or rewrites
		{"a", "b.test."},
		{"a", "b..test"},
		{"Ada <a", "b.test>"},
		{`"a b"`, "c.test"},
		{"", ""},
	} {
		f.Add(s.local, s.host)
	}

	// The address is fuzzed as its two halves so mutations land in the
	// host name, which is the rule under test, instead of mostly breaking
	// the '@' that mail.ParseAddress needs.
	f.Fuzz(func(t *testing.T, local, host string) {
		if len(local)+len(host) > 1024 {
			return // the bounds are at 254 bytes; longer only re-measures them
		}
		addr := local + "@" + host
		parsed, err := mail.ParseAddress(addr)
		if err != nil || parsed.Address != addr {
			return // validateRegistration refuses it before the host rule
		}

		reason := emailHostRefusal(addr)
		zitadelErr := pgvEmail(addr)

		// Agreement, both ways: refused by pyck <=> refused by Zitadel.
		if reason == "" {
			require.NoError(t, zitadelErr, "pyck accepts %q, which Zitadel refuses", addr)
		} else {
			require.Error(t, zitadelErr, "pyck refuses %q (%s), which Zitadel accepts", addr, reason)
		}
	})
}

// FuzzValidateRegistrationPassword guards the password's exemption from the
// whitespace and blank rules, and the one rule it keeps. Every password the
// complexity rule accepts must be accepted by validateRegistration exactly
// when it holds no control rune: a refusal of anything else turns away a
// password Zitadel would store, and an acceptance of a control rune lets a
// password through that no login form can reproduce. A refusal names the
// field with a fixed text and never echoes the password.
//
// Run continuously with:
//
//	go test ./backend/management/resolvers/ -run '^$' -fuzz FuzzValidateRegistrationPassword
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzValidateRegistrationPassword(f *testing.F) {
	for _, s := range []string{
		seedPassword, " " + seedPassword, seedPassword + " ", "Str0ng! pass",
		"\u00a0" + seedPassword + "\u00a0", "\u3000" + seedPassword, "Aaaaaaa1 ",
		"        Aa1", "Aa1\u2028\u2029xxxx", "Aa1\ufeffxxxx", "Aa1\u200bxxxx",
		seedPassword + "\x00", "\x00" + seedPassword, seedPassword + "\n",
		seedPassword + "\u0085", seedPassword + "\u009b", seedPassword + "\x7f",
		"Aa1!\xff\xfe\xfd\xfc", "Aa1!" + strings.Repeat("\u00e9", 34),
	} {
		f.Add(s)
	}

	want := errRegistrationInvalid.Error() + ": adminPassword contains a control character"
	f.Fuzz(func(t *testing.T, password string) {
		if !isValidPassword(password) {
			return // registerTenant refuses it with errPasswordInvalid first
		}
		in := validRegistration()
		in.AdminPassword = password

		err := validateRegistration(in)

		if !strings.ContainsFunc(password, unicode.IsControl) {
			require.NoError(t, err, "a password free of control runes is refused: %q", password)
			return
		}
		require.ErrorIs(t, err, errRegistrationInvalid)
		require.Equal(t, want, err.Error(), "the refusal is the fixed password text, never the password")
	})
}

// decodeLikeTheTransport decodes a request body the way the running
// server does, which is not the way a hand-written test usually does.
// gqlgen's HTTP transport decodes every request (POST and GET alike)
// through one json.Decoder with UseNumber set
// (graphql/handler/transport/http_get.go:jsonDecode, used by http_post.go
// too), and graphql.UnmarshalMap hands the Map scalar's value through
// untouched. So a number in a GraphQL *variable* reaches the resolver as
// a json.Number holding the caller's own text, never as a float64.
//
// Modelling the input with a plain json.Unmarshal (which yields float64
// and rejects on the resolver's behalf every literal float64 cannot hold)
// makes the validator look total when it is not. That is finding F1.
func decodeLikeTheTransport(raw string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()

	var data map[string]any
	if err := dec.Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

// FuzzValidateTenantData guards the resolver/row/activity contract of the
// caller's data map: validateTenantData is the only gate between it and
// two things that must both survive it: the tenants row it is written
// to, and SetOrgMetadataActivity, which renders it after the org exists
// (registertenant.metadataValue handles a string, a float64, a bool and a
// json.Number, nothing else).
//
// The map crosses JSON twice on the way: into the tenant row's data
// column, and out of it again for every reader of the row: the outbox
// payload the tenant-lifecycle trigger decodes, the LoadTenantRegistration
// activity, and the resolver's own final read. A value the row accepts
// but cannot be read back out of is worse than a refusal: the row is
// committed, holds the tenant name (reserved across every row until a
// hard delete), and every path that would clean it up (the trigger,
// deleteTenant, restoreTenant) fails to decode it. That crossing is therefore asserted
// in both directions, over the input the transport really produces.
//
// Run continuously with:
//
//	go test ./backend/management/resolvers/ -run '^$' -fuzz FuzzValidateTenantData
//
// Under a plain `go test` the seed corpus below runs as ordinary regression.
func FuzzValidateTenantData(f *testing.F) {
	seeds := []string{
		// real maps
		`{"flavour":"pyck-go","seats":12,"trial":true}`,
		`{}`,
		`{"seats":1000000}`,
		// number shapes: the exponent forms are what the activity's
		// renderer exists for
		`{"a":1e6,"b":1e21,"c":1e-7,"d":-0,"e":0.1,"f":12345678901234567890}`,
		`{"a":1e308,"b":-1e308}`,
		// magnitudes float64 has no room for. Valid JSON, valid GraphQL
		// variables, and the row stores the caller's text verbatim.
		`{"a":1e400}`, `{"a":-1e400}`, `{"a":1e-400}`,
		`{"a":1` + strings.Repeat("0", 400) + `}`,
		// shapes the gate must refuse
		`{"a":null}`, `{"a":{"b":1}}`, `{"a":[1,2]}`, `{"a":""}`, `{"":"v"}`,
		// duplicate keys, unicode keys and values, escapes
		`{"a":"1","a":"2"}`,
		`{"\u043a\u043b\u044e\u0447":"v","emoji":"\ud83d\ude42","zw":"a\u200bb"}`,
		`{"a":"\u0000\u0001"}`,
		// NUL that Postgres refuses in jsonb (22P05), in a value and a key
		`{"k":"a\u0000b"}`, `{"k\u0000":"v"}`, `{"\u0000":"\u0000"}`,
		`{"a":"<script>&"}`,
		// not an object at all
		`[]`, `"x"`, `null`, `1`, ``,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		// Bound the input: the size rule is checked at 16 KiB, so a
		// megabyte only re-measures json.Marshal.
		if len(raw) > 64*1024 {
			return
		}

		data, err := decodeLikeTheTransport(raw)
		if err != nil {
			return // gqlgen refuses a Map variable that is not an object
		}
		if err := validateTenantData(data); err != nil {
			return // refused input never reaches the row or the activity
		}

		// 1. Accepted ⟹ the bounds the row and the workflow history rely
		//    on hold.
		require.LessOrEqual(t, len(data), maxTenantDataKeys)
		for key := range data {
			require.NotEmpty(t, key)
			require.LessOrEqual(t, len(key), maxTenantDataKeyBytes)
		}

		// 2. Accepted ⟹ the map is writable as the row's data column.
		//    The column is jsonb, which refuses U+0000 in a key or a
		//    string (22P05) although encoding/json writes it as \u0000
		//    without complaint, so the marshal alone does not model the
		//    row.
		for key, value := range data {
			require.NotContains(t, key, "\x00", "BYPASS: accepted a key whose NUL the jsonb column refuses")
			if v, ok := value.(string); ok {
				require.NotContains(t, v, "\x00", "BYPASS: accepted %q holding a NUL the jsonb column refuses", key)
			}
		}
		stored, err := json.Marshal(data)
		require.NoError(t, err, "accepted a map the row cannot store")
		require.LessOrEqual(t, len(stored), maxTenantDataBytes)

		// 3. Accepted ⟹ the committed row can be read back. Every reader
		//    of the row (ent's own scanner, the outbox payload the
		//    tenant-lifecycle trigger decodes, the registration run's
		//    LoadTenantRegistration step) uses a plain encoding/json
		//    decode into map[string]any. A row that fails here is a row
		//    nothing can process and nothing can delete, holding the
		//    tenant name for good.
		var loaded map[string]any
		require.NoError(t, json.Unmarshal(stored, &loaded),
			"BYPASS: accepted a map the row stores as %s, which no reader of the row can decode", stored)

		// 4. Accepted ⟹ what comes back out is still something
		//    metadataValue renders, and no string became empty on the way.
		for key, value := range loaded {
			switch v := value.(type) {
			case string:
				require.NotEmpty(t, v, "key %q holds an empty value Zitadel refuses", key)
			case float64, bool:
			default:
				t.Fatalf("BYPASS: accepted %q of type %T, which metadataValue refuses", key, value)
			}
		}

		// 5. Accepted ⟹ still accepted after the round-trip. The validated
		//    map and the map the workflow loads are not the same object,
		//    and a rule that holds only for the first is not a rule.
		require.NoError(t, validateTenantData(loaded),
			"a map accepted before the row was written is refused after reading it back")
	})
}
