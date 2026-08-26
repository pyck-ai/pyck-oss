package resolvers

import (
	"context"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/vikstrous/dataloadgen"

	"github.com/pyck-ai/pyck/backend/common/ent/mixin"
	"github.com/pyck-ai/pyck/backend/common/request"

	ent "github.com/pyck-ai/pyck/backend/inventory/ent/gen"
	entpredicate "github.com/pyck-ai/pyck/backend/inventory/ent/gen/predicate"
	entstock "github.com/pyck-ai/pyck/backend/inventory/ent/gen/stock"
)

// stockKey is a (repository, item) pair. Tenant is excluded: it comes from the
// request context in the batch function, so every key in a batch shares a scope.
type stockKey struct {
	RepositoryID uuid.UUID
	ItemID       uuid.UUID
}

// Loaders holds the per-request dataloaders. A fresh instance per operation
// keeps the loader cache from leaking across tenant scopes.
type Loaders struct {
	CurrentStock *dataloadgen.Loader[stockKey, *ent.Stock]
}

func newLoaders(client *ent.Client) *Loaders {
	return &Loaders{
		// WithWait(0): gqltx serializes field resolvers behind a per-request
		// mutex, so a batch window can never fill — it would only add its
		// duration to every resolution. Dedup still applies, and batching
		// starts working for free if resolvers ever stop being serialized.
		CurrentStock: dataloadgen.NewLoader(currentStockBatchFunc(client), dataloadgen.WithWait(0)),
	}
}

// currentStockBatchFunc resolves every key in a batch with one query. Batches
// hold a single key today (resolvers are serialized — see newLoaders), so this
// is also the direct-fetch path.
func currentStockBatchFunc(client *ent.Client) func(context.Context, []stockKey) ([]*ent.Stock, []error) {
	return func(ctx context.Context, keys []stockKey) ([]*ent.Stock, []error) {
		byKey, err := loadCurrentStockRows(ctx, client, keys)
		if err != nil {
			return make([]*ent.Stock, len(keys)), []error{err} // broadcast to every key
		}

		out := make([]*ent.Stock, len(keys))
		for i, k := range keys {
			out[i] = byKey[k] // nil → GraphQL null when the pair holds no current stock
		}
		return out, nil
	}
}

// loadCurrentStockRows returns the current stock row for each requested
// (repository, item) pair, keyed for lookup. A deleted head yields no entry, so
// its pair reports no current stock — matching CurrentStocks.
func loadCurrentStockRows(ctx context.Context, client *ent.Client, keys []stockKey) (map[stockKey]*ent.Stock, error) {
	if len(keys) == 0 {
		return map[stockKey]*ent.Stock{}, nil
	}

	repoSet := make(map[uuid.UUID]struct{}, len(keys))
	itemSet := make(map[uuid.UUID]struct{}, len(keys))
	for _, k := range keys {
		repoSet[k.RepositoryID] = struct{}{}
		itemSet[k.ItemID] = struct{}{}
	}

	scope := []entpredicate.Stock{
		entstock.TenantIDIn(request.ForContext(ctx).TenantIDs()...),
		entstock.RepositoryIDIn(mapKeys(repoSet)...),
		entstock.ItemIDIn(mapKeys(itemSet)...),
	}

	rows, err := client.Stock.Query().
		Where(scope...).
		Where(stockHeadPredicate(scope...)).
		AllPages(ctx, mixin.Limit)
	if err != nil {
		return nil, err
	}

	byKey := make(map[stockKey]*ent.Stock, len(rows))
	for _, r := range rows {
		byKey[stockKey{RepositoryID: r.RepositoryID, ItemID: r.ItemID}] = r
	}
	return byKey, nil
}

func mapKeys(m map[uuid.UUID]struct{}) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

type loadersCtxKey struct{}

func withLoaders(ctx context.Context, l *Loaders) context.Context {
	return context.WithValue(ctx, loadersCtxKey{}, l)
}

// loadersForContext returns the request's loaders, or nil when no extension is
// installed (callers then fetch directly, unbatched).
func loadersForContext(ctx context.Context) *Loaders {
	l, _ := ctx.Value(loadersCtxKey{}).(*Loaders)
	return l
}

// LoaderExtension installs a fresh set of per-request loaders, mirroring how
// gqltx injects its transaction.
type LoaderExtension struct {
	client *ent.Client
}

func NewLoaderExtension(client *ent.Client) LoaderExtension {
	return LoaderExtension{client: client}
}

func (LoaderExtension) ExtensionName() string { return "InventoryLoaders" }

func (LoaderExtension) Validate(graphql.ExecutableSchema) error { return nil }

// InterceptResponse, not InterceptOperation: gqlgen invokes the response
// handler with the transport's context, so values added around an operation
// interceptor's `next` never reach the resolvers. gqltx injects its
// transaction here for the same reason.
func (e LoaderExtension) InterceptResponse(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	return next(withLoaders(ctx, newLoaders(e.client)))
}
