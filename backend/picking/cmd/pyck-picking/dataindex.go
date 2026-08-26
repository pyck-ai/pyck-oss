package main

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/pyck-ai/pyck/backend/common/dataindex"
	json_schema "github.com/pyck-ai/pyck/backend/common/json-schema"
	"github.com/pyck-ai/pyck/backend/common/log"
)

// backfillBatchSize bounds one UPDATE so a large table is never locked at once.
const backfillBatchSize = 500

// backfillDataIndices projects data into the indexed slots of orders written
// before their datatype declared a binding. Without it the projection hook, which
// only fires on writes, leaves older rows unindexed and lookups silently miss them.
//
// It reads the datatypes from the same listing that warms the schema cache at
// boot: unlike the cache's per-slug reads, that listing needs no request context,
// and it carries the tenant each binding belongs to.
func backfillDataIndices(ctx context.Context, db *sql.DB, dataTypes []json_schema.DataType) error {
	pool, _ := dataindex.PoolFor(dataindex.EntityPickingOrder)
	for _, dt := range dataTypes {
		// A schema that will not unmarshal cannot carry a usable binding, so it
		// has nothing to backfill. Skipping it keeps one malformed row -- a
		// pre-validation leftover on any single tenant -- from failing boot for
		// every tenant on every restart. Fail-boot stays for the SQL errors of
		// real bindings, which is where a silent miss would actually hide.
		bindings, err := dataindex.Parse(dt.JsonSchema)
		if err != nil {
			log.ForContext(ctx).Warn().Err(err).
				Str("dataType", dt.Slug).Str("tenant", dt.TenantID.String()).
				Msg("skipping data index backfill: datatype schema does not parse")
			continue
		}
		// The bindings come from another service's datatype row. Backfill writes
		// the slot column by name, so drop any that names a slot this entity does
		// not have -- that binding is broken regardless, and skipping it must not
		// take down the rest of the pass.
		bindings = keepPoolSlots(ctx, dt.Slug, bindings, pool)
		if len(bindings) == 0 {
			continue
		}
		n, err := dataindex.Backfill(ctx, db, "picking", "orders", dt.TenantID, dt.Slug, bindings, backfillBatchSize)
		if err != nil {
			return fmt.Errorf("datatype %q: %w", dt.Slug, err)
		}
		if n > 0 {
			log.ForContext(ctx).Info().
				Str("dataType", dt.Slug).Str("tenant", dt.TenantID.String()).Int("rows", n).
				Msg("backfilled data index slots")
		}
	}
	return nil
}

// keepPoolSlots returns the bindings whose slot the entity actually has, warning
// on any it drops.
func keepPoolSlots(ctx context.Context, slug string, bindings dataindex.Bindings, pool dataindex.SlotPool) dataindex.Bindings {
	kept := make(dataindex.Bindings, len(bindings))
	for name, b := range bindings {
		if pool.Has(b.Slot) {
			kept[name] = b
			continue
		}
		log.ForContext(ctx).Warn().
			Str("dataType", slug).Str("index", name).Str("slot", b.Slot).
			Msg("skipping backfill for index bound to an unknown slot")
	}
	return kept
}

// backfillOnDataTypeChange returns the cache hook that projects a datatype's
// slots when its bindings change while the service is running.
//
// The boot pass alone is not enough: the rollout applies x-indices through the
// API against a live stack, so without this, rows written before the binding
// would stay unindexed -- and a claim lookup would miss them with no signal --
// until someone restarted the pod.
//
// Passes are serialized per datatype: a burst of events (a seeding import) would
// otherwise put one full table walk per event on the database at once.
func backfillOnDataTypeChange(ctx context.Context, db *sql.DB) func(json_schema.DataType) {
	var mu sync.Mutex
	running := map[string]*sync.Mutex{}

	return func(dt json_schema.DataType) {
		key := dt.TenantID.String() + "/" + dt.Slug
		mu.Lock()
		lock, ok := running[key]
		if !ok {
			lock = &sync.Mutex{}
			running[key] = lock
		}
		mu.Unlock()

		go func() {
			lock.Lock()
			defer lock.Unlock()
			// Runs on the NATS consumer goroutine's behalf, so a failure cannot
			// fail boot; it is logged loudly and the next boot retries the pass.
			if err := backfillDataIndices(ctx, db, []json_schema.DataType{dt}); err != nil {
				log.ForContext(ctx).Error().Err(err).
					Str("dataType", dt.Slug).Str("tenant", dt.TenantID.String()).
					Msg("data index backfill after datatype change failed; lookups may miss rows until the next boot")
			}
		}()
	}
}
