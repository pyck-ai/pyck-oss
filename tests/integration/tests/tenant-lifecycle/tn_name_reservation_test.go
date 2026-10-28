//go:build integration

package tenants

import (
	"fmt"
	"strings"
	"sync"

	"github.com/pyck-ai/pyck/backend/management/ent/types"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// TnNameReservationSuite pins that tenant names are compared by their
// Zitadel-style case fold (name_key) and stay reserved by every row, soft-deleted ones
// included. Zitadel compares org names without case, so a case variant
// that passed pyck's checks would end as a permanent error row; and a
// retired tenant's name, were it free, would let anyone block its
// restoreTenant with a duplicate.
type TnNameReservationSuite struct {
	tests.Base
}

// nameRowCount counts every tenant row whose name_key is name's, deleted
// ones included: the vantage point the API cannot give, since the tenants
// query hides soft-deleted rows and matches names exactly.
func (s *TnNameReservationSuite) nameRowCount(name string) int {
	s.T().Helper()
	out, err := tests.Psql(s.Ctx, fmt.Sprintf(
		`SELECT count(*) FROM management.tenants WHERE name_key = '%s'`, strings.ReplaceAll(types.TenantNameKey(name), "'", "''")))
	s.Require().NoError(err)
	var n int
	_, err = fmt.Sscan(out, &n)
	s.Require().NoError(err, "count output %q", out)
	return n
}

// A live tenant's name in another case is refused before any row is
// inserted: no error row is left holding the variant.
func (s *TnNameReservationSuite) TestRegister_CaseVariantOfALiveNameIsRefused() {
	r := s.Require()
	first := fixtures.NewTenant()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, first)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	for _, variant := range []string{strings.ToUpper(first.Name), strings.ToUpper(first.Name[:1]) + first.Name[1:]} {
		second := fixtures.NewTenant()
		second.Name = variant

		dup, err := gateway.RegisterTenant(s.Ctx, s.Cfg, second)
		r.Error(err, "%q differs from %q only in case", variant, first.Name)
		r.Nil(dup)
		r.Contains(err.Error(), "already exists")
		r.NotContains(err.Error(), "marked error", "refused before a row or a run exists")
	}

	r.Equal(1, s.nameRowCount(first.Name), "only the first registration holds a row")
}

// Disabling a tenant does not free its name: a registration of the name in
// another case is refused while the tenant is retired, and restoreTenant
// brings the tenant back under its own name.
func (s *TnNameReservationSuite) TestRegister_RetiredNameStaysReservedForRestore() {
	r := s.Require()
	original := fixtures.NewTenant()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, original)
	r.NoError(err)
	s.DeferTenantCleanup(rt.ID)

	r.NoError(gateway.DeleteTenant(s.Ctx, s.Cfg, rt.ID))

	for _, name := range []string{original.Name, strings.ToUpper(original.Name)} {
		taker := fixtures.NewTenant()
		taker.Name = name
		got, err := gateway.RegisterTenant(s.Ctx, s.Cfg, taker)
		r.Error(err, "the retired tenant's name %q must stay reserved", name)
		r.Nil(got)
		r.Contains(err.Error(), "already exists")
	}
	r.Equal(1, s.nameRowCount(original.Name), "the refused registrations inserted nothing")

	r.NoError(gateway.RestoreTenant(s.Ctx, s.Cfg, rt.ID), "nothing took the name, so the restore must succeed")
	status, err := tests.TenantStatus(s.Ctx, s.Cfg, s.Cfg.ServiceToken, rt.ID)
	r.NoError(err, "the restored tenant is listed again")
	r.Equal("done", status)
}

// Postgres' lower() under the stack's libc locale keeps the circled
// letters apart, while Zitadel's Go fold lowercases them: "ⓘntegrations-…"
// next to a live "Ⓘntegrations-…" is the same org to Zitadel. name_key
// folds the Go way, so the variant is refused as "already exists" before
// a row or a run exists, instead of ending as a permanent error row.
func (s *TnNameReservationSuite) TestRegister_GoFoldedVariantOfALiveNameIsRefused() {
	r := s.Require()
	first := fixtures.NewTenant()
	first.Name = "Ⓘ" + first.Name[1:] // CIRCLED LATIN CAPITAL LETTER I
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, first)
	r.NoError(err, "a circled-letter name is an ordinary registration")
	s.DeferTenantCleanup(rt.ID)

	second := fixtures.NewTenant()
	second.Name = "ⓘ" + first.Name[len("Ⓘ"):] // CIRCLED LATIN SMALL LETTER I
	dup, err := gateway.RegisterTenant(s.Ctx, s.Cfg, second)
	if found, findErr := gateway.FindTenantByName(s.Ctx, s.Cfg, second.Name); findErr == nil {
		s.DeferTenantCleanup(found.ID) // only on a red run
	}

	r.Error(err, "%q folds to the same org name as %q", second.Name, first.Name)
	r.Nil(dup)
	r.Contains(err.Error(), "already exists")
	r.NotContains(err.Error(), "marked error", "refused before a row or a run exists")
	r.Equal(1, s.nameRowCount(first.Name), "only the first registration holds the folded name")
}

// Concurrent registrations of names that fold to one name_key are decided
// by the tenant_name_key index, not by the pre-flight each of them passed
// before the others committed: exactly one wins, every other one is
// answered "already exists" (never a serialization failure or driver text
// from its pending insert), and no error row is left holding a variant.
// The three requests stay under the per-replica admission cap, so none of
// them is refused for concurrency.
func (s *TnNameReservationSuite) TestRegister_ConcurrentFoldedVariantsHaveOneWinner() {
	r := s.Require()
	base := fixtures.NewTenant().Name[1:]
	names := []string{"Ⓘ" + base, "ⓘ" + base, "ⓘ" + strings.ToUpper(base)}

	type outcome struct {
		tenant *gateway.RegisteredTenant
		err    error
	}
	outcomes := make([]outcome, len(names))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := fixtures.NewTenant()
			t.Name = name
			<-start
			rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, t)
			outcomes[i] = outcome{rt, err}
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, o := range outcomes {
		if o.err == nil {
			winners++
			s.DeferTenantCleanup(o.tenant.ID)
			continue
		}
		r.Nil(o.tenant)
		r.Contains(o.err.Error(), "already exists", "%q lost the race and must get the friendly refusal", names[i])
		for _, leak := range []string{"serialize", "SQLSTATE", "pq:", "insert pending tenant", "marked error"} {
			r.NotContains(o.err.Error(), leak, "%q", names[i])
		}
	}
	r.Equal(1, winners, "exactly one of %q holds the folded name", names)
	r.Equal(1, s.nameRowCount(names[0]), "the losers inserted no row, error rows included")
}
