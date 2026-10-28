package tenant_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/liveness"
	livenessmocks "github.com/pyck-ai/pyck/backend/common/liveness/mocks"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/tenant"
)

// serve runs one request with the given header value ("" = no header) as
// user through a middleware built on cache+source and returns the recorder
// plus the tenant set the downstream handler saw (nil when not reached).
func serveThroughMiddleware(t *testing.T, cache *liveness.Cache, source liveness.TenantSource, user *authn.User, header string) (*httptest.ResponseRecorder, []uuid.UUID) {
	t.Helper()
	var seen []uuid.UUID
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		seen = tenant.ForContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	if header != "" {
		req.Header.Set(tenant.TenantIDHeader, header)
	}
	if user != nil {
		req = req.WithContext(authn.Context(req.Context(), user))
	}
	rec := httptest.NewRecorder()
	newMiddleware(t, cache, source)(next).ServeHTTP(rec, req)
	if !reached {
		return rec, nil
	}
	return rec, seen
}

func crossTenantUser(home, granted uuid.UUID) *authn.User {
	return &authn.User{
		ID:       uuid.New(),
		TenantID: home,
		Roles: map[uuid.UUID]authn.Role{
			home:    authn.ROLE_WRITER,
			granted: authn.ROLE_WRITER,
		},
	}
}

func disabledSource(ids ...uuid.UUID) *livenessmocks.Source {
	src := &livenessmocks.Source{Disabled: map[uuid.UUID]struct{}{}}
	for _, id := range ids {
		src.Disabled[id] = struct{}{}
	}
	return src
}

func TestMiddleware_DisabledTenant(t *testing.T) {
	t.Parallel()
	home, granted := uuid.New(), uuid.New()
	user := crossTenantUser(home, granted)

	t.Run("explicit disabled tenant is denied with 403", func(t *testing.T) {
		t.Parallel()
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), disabledSource(granted).Lookup, user, granted.String())

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), tenant.ErrTenantDisabled.Error())
		assert.Contains(t, rec.Body.String(), granted.String())
		assert.Nil(t, seen, "handler must not run")
	})

	t.Run("explicit list with one disabled tenant denies the whole request", func(t *testing.T) {
		t.Parallel()
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), disabledSource(granted).Lookup, user, home.String()+","+granted.String())

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Nil(t, seen)
	})

	t.Run("mixed all plus explicit disabled tenant is denied", func(t *testing.T) {
		t.Parallel()
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), disabledSource(granted).Lookup, user, "all,"+granted.String())

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Nil(t, seen)
	})

	t.Run("all filters the disabled tenant out and proceeds", func(t *testing.T) {
		t.Parallel()
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), disabledSource(granted).Lookup, user, "all")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []uuid.UUID{home}, seen)
	})

	t.Run("no header defaults to all and filters the disabled tenant out", func(t *testing.T) {
		t.Parallel()
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), disabledSource(granted).Lookup, user, "")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []uuid.UUID{home}, seen)
	})

	t.Run("all with every tenant disabled is denied instead of proceeding empty", func(t *testing.T) {
		t.Parallel()
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), disabledSource(home, granted).Lookup, user, "all")

		assert.Equal(t, http.StatusForbidden, rec.Code,
			"an empty acting set would surface downstream as a misleading missing-role or no-target error")
		assert.Contains(t, rec.Body.String(), tenant.ErrTenantDisabled.Error())
		assert.Nil(t, seen)
	})

	t.Run("explicit live tenant is unaffected by another disabled tenant", func(t *testing.T) {
		t.Parallel()
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), disabledSource(granted).Lookup, user, home.String())

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []uuid.UUID{home}, seen)
	})

	t.Run("source sees the whole acting set including the home tenant", func(t *testing.T) {
		t.Parallel()
		src := disabledSource()
		rec, _ := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), src.Lookup, user, "all")

		assert.Equal(t, http.StatusOK, rec.Code)
		require.Len(t, src.Batches, 1)
		assert.ElementsMatch(t, []uuid.UUID{home, granted}, src.Batches[0])
	})

	t.Run("role check runs before the liveness check", func(t *testing.T) {
		t.Parallel()
		stranger := uuid.New()
		src := disabledSource(stranger)
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), src.Lookup, user, stranger.String())

		assert.Equal(t, http.StatusBadRequest, rec.Code, "no role on the tenant → 400, not 403")
		assert.Contains(t, rec.Body.String(), tenant.ErrNoAccessToTenantID.Error())
		assert.Nil(t, seen)
		assert.Empty(t, src.Batches, "a caller without a grant must not learn the tenant's state")
	})

	t.Run("source fault denies tenants with no cached verdict", func(t *testing.T) {
		t.Parallel()
		src := &livenessmocks.Source{Err: errors.New("management unreachable")}
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), src.Lookup, user, granted.String())

		assert.Equal(t, http.StatusForbidden, rec.Code,
			"a tenant that cannot be verified is not served")
		assert.Nil(t, seen)
	})

	t.Run("source fault keeps cached verdicts of both signs in force", func(t *testing.T) {
		t.Parallel()
		cache := livenessmocks.NewTestCache(t)
		src := disabledSource(granted)
		rec, _ := serveThroughMiddleware(t, cache, src.Lookup, user, granted.String())
		require.Equal(t, http.StatusForbidden, rec.Code,
			"verdicts for the granted and home tenants are now cached")

		fresh := uuid.New()
		user := &authn.User{ID: uuid.New(), TenantID: home, Roles: map[uuid.UUID]authn.Role{
			home: authn.ROLE_WRITER, granted: authn.ROLE_WRITER, fresh: authn.ROLE_WRITER,
		}}
		src.Err = errors.New("management unreachable")
		rec, seen := serveThroughMiddleware(t, cache, src.Lookup, user, "all")
		assert.Equal(t, http.StatusOK, rec.Code,
			"the cached-live home tenant keeps the caller served through the fault")
		assert.ElementsMatch(t, []uuid.UUID{home}, seen,
			"the cached-disabled tenant stays filtered and the never-seen one is denied")
	})

	t.Run("caller's home tenant is checked even when not acted on", func(t *testing.T) {
		t.Parallel()
		src := disabledSource(home)
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), src.Lookup, user, granted.String())

		assert.Equal(t, http.StatusForbidden, rec.Code, "a disabled tenant's users lose access everywhere")
		assert.Contains(t, rec.Body.String(), home.String())
		assert.Nil(t, seen)
		require.Len(t, src.Batches, 1)
		assert.ElementsMatch(t, []uuid.UUID{granted, home}, src.Batches[0])
	})

	t.Run("system user bypasses the liveness check", func(t *testing.T) {
		t.Parallel()
		src := disabledSource(granted)
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), src.Lookup, authn.SystemUser(), granted.String())

		assert.Equal(t, http.StatusOK, rec.Code, "system callers disable and restore tenants; they must reach a disabled tenant")
		assert.Equal(t, []uuid.UUID{granted}, seen)
		assert.Empty(t, src.Batches)
	})

	t.Run("unauthenticated request without header never consults the source", func(t *testing.T) {
		t.Parallel()
		src := disabledSource()
		rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), src.Lookup, nil, "")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Empty(t, seen)
		assert.Empty(t, src.Batches)
	})
}

// The middleware serves verdicts of both signs from the liveness cache under
// liveness.TenantKey and asks the source only for misses, in one batch.
func TestMiddleware_LivenessCache(t *testing.T) {
	t.Parallel()

	t.Run("both signs are cached; only misses reach the source", func(t *testing.T) {
		t.Parallel()
		home, granted, fresh := uuid.New(), uuid.New(), uuid.New()
		user := &authn.User{ID: uuid.New(), TenantID: home, Roles: map[uuid.UUID]authn.Role{
			home: authn.ROLE_WRITER, granted: authn.ROLE_WRITER, fresh: authn.ROLE_WRITER,
		}}
		cache := livenessmocks.NewTestCache(t)
		src := disabledSource(granted)

		rec, _ := serveThroughMiddleware(t, cache, src.Lookup, user, home.String()+","+granted.String())
		assert.Equal(t, http.StatusForbidden, rec.Code)
		require.Equal(t, 1, src.Calls())

		rec, _ = serveThroughMiddleware(t, cache, src.Lookup, user, home.String()+","+granted.String())
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, 1, src.Calls(), "a request that keeps naming a disabled tenant must not hit the source again")

		rec, seen := serveThroughMiddleware(t, cache, src.Lookup, user, "all")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.ElementsMatch(t, []uuid.UUID{home, fresh}, seen)
		require.Equal(t, 2, src.Calls())
		assert.Equal(t, []uuid.UUID{fresh}, src.Batches[1], "only the never-seen tenant goes to the source")

		live, ok := cache.Get(liveness.TenantKey(granted))
		require.True(t, ok)
		assert.False(t, live, "verdicts are stored under the tenant URN")
	})

	t.Run("a lifecycle event evicts; the next request re-reads the source", func(t *testing.T) {
		t.Parallel()
		home, granted := uuid.New(), uuid.New()
		user := crossTenantUser(home, granted)
		cache := livenessmocks.NewTestCache(t)
		src := disabledSource()

		rec, _ := serveThroughMiddleware(t, cache, src.Lookup, user, granted.String())
		assert.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, 1, src.Calls())

		// deleteTenant → tenant CRUD event → eviction → the next request
		// consults management, which now reports the tenant disabled.
		src.Disabled[granted] = struct{}{}
		cache.EvictForTenantEvent(granted)
		rec, _ = serveThroughMiddleware(t, cache, src.Lookup, user, granted.String())
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Equal(t, 2, src.Calls())

		// restoreTenant → event → eviction → re-read → admitted again.
		delete(src.Disabled, granted)
		cache.EvictForTenantEvent(granted)
		rec, _ = serveThroughMiddleware(t, cache, src.Lookup, user, granted.String())
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, 3, src.Calls())
	})

	t.Run("a lookup overlapping an event is not stored", func(t *testing.T) {
		t.Parallel()
		home, granted := uuid.New(), uuid.New()
		user := crossTenantUser(home, granted)
		cache := livenessmocks.NewTestCache(t)
		src := disabledSource()
		racing := func(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
			out, err := src.Lookup(ctx, ids)
			// The disable commits and its event lands while the lookup is out.
			src.Disabled[granted] = struct{}{}
			cache.EvictForTenantEvent(granted)
			return out, err
		}

		rec, _ := serveThroughMiddleware(t, cache, racing, user, granted.String())
		assert.Equal(t, http.StatusOK, rec.Code, "the in-flight answer itself is what the source said at read time")

		rec, _ = serveThroughMiddleware(t, cache, src.Lookup, user, granted.String())
		assert.Equal(t, http.StatusForbidden, rec.Code,
			"the overlapping 'enabled' answer must not have been cached; the re-read sees the disable")
	})
}

// The acting set lists explicitly named tenants first, in header order,
// followed by the "all" expansion, without duplicates — the order error
// messages and logs rely on.
func TestParseHeaders_ExplicitFirstInHeaderOrder(t *testing.T) {
	t.Parallel()
	home, other := uuid.New(), uuid.New()
	ctx := authn.Context(t.Context(), crossTenantUser(home, other))

	h := http.Header{}
	h.Add(tenant.TenantIDHeader, "all,"+other.String())
	h.Add(tenant.TenantIDHeader, other.String()+", "+home.String())

	got, err := tenant.ParseHeaders(ctx, h)
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{other, home}, got)

	h = http.Header{}
	h.Set(tenant.TenantIDHeader, "all,"+other.String())
	got, err = tenant.ParseHeaders(ctx, h)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, other, got[0], "explicit tenant must precede the all-expanded one")
	assert.ElementsMatch(t, []uuid.UUID{other, home}, got)
}

// The role axis is all-or-nothing over the WHOLE acting set, "all"
// expansion included — the pre-liveness behaviour: a Roles entry below
// READER (a project role literally named "none") fails the entire request
// rather than being filtered out, so it can neither act on data nor slip
// through the default header silently.
func TestMiddleware_AllExpansionRoleCheckIsAllOrNothing(t *testing.T) {
	t.Parallel()

	home, noneTenant := uuid.New(), uuid.New()
	user := &authn.User{
		ID:       uuid.New(),
		TenantID: home,
		Roles: map[uuid.UUID]authn.Role{
			home:       authn.ROLE_WRITER,
			noneTenant: authn.ROLE_NONE,
		},
	}
	src := disabledSource()
	rec, seen := serveThroughMiddleware(t, livenessmocks.NewTestCache(t), src.Lookup, user, "all")

	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"one sub-READER tenant in the expansion denies the whole request (main parity)")
	assert.Contains(t, rec.Body.String(), tenant.ErrNoAccessToTenantID.Error())
	assert.Nil(t, seen)
	assert.Empty(t, src.Batches, "the role axis denies before any liveness lookup")

	rec, seen = serveThroughMiddleware(t, livenessmocks.NewTestCache(t), src.Lookup, user, noneTenant.String())
	assert.Equal(t, http.StatusBadRequest, rec.Code, "naming it explicitly fails the same way")
	assert.Nil(t, seen)
}

// Filtering a disabled tenant out of the "all" expansion also settles the
// request's mutation target: mutations require an acting set of exactly one
// tenant, and the acting set is what survives the liveness axis. A caller
// with grants in two tenants, one of them disabled, is left with a single
// live tenant and mutates it implicitly — where the two-tenant set had no
// unambiguous target at all. Recorded in docs/tenant-access-control.md.
func TestMiddleware_DisabledTenantSettlesMutationTarget(t *testing.T) {
	t.Parallel()
	home, granted := uuid.New(), uuid.New()
	user := crossTenantUser(home, granted)

	// serve runs one no-header request (the "all" default) and returns the
	// request context the downstream handler resolved.
	serve := func(t *testing.T, source liveness.TenantSource) request.RequestContext {
		t.Helper()
		var resolved request.RequestContext
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resolved = request.ForContext(r.Context())
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequestWithContext(authn.Context(t.Context(), user), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		newMiddleware(t, livenessmocks.NewTestCache(t), source)(next).ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		return resolved
	}

	t.Run("two live tenants leave no unambiguous mutation target", func(t *testing.T) {
		t.Parallel()
		resolved := serve(t, disabledSource().Lookup)

		assert.Len(t, resolved.TenantIDs(), 2)
		assert.False(t, resolved.HasMutationTenantID())
	})

	t.Run("disabling one of them makes the other the implicit target", func(t *testing.T) {
		t.Parallel()
		resolved := serve(t, disabledSource(granted).Lookup)

		require.True(t, resolved.HasMutationTenantID(),
			"the acting set is what the request may act on; one live tenant is an unambiguous target")
		target, err := resolved.MutationTenantID()
		require.NoError(t, err)
		assert.Equal(t, home, target)
	})
}
