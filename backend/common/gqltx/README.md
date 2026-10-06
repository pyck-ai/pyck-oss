# gqltx

Transaction middleware for GraphQL servers. Wraps every GraphQL operation in a
single database transaction, injects it into the resolver context (so all Ent
queries inside the operation share the tx), and commits or rolls back when the
operation completes.

## Reader/writer routing

As of Phase 8.2, the middleware splits operations across the database pools by
operation kind:

- **Mutations** begin their tx with `nil` `*sql.TxOptions`. They run on the
  writer pool at the service's default isolation level (SERIALIZABLE elsewhere,
  READ COMMITTED in `inventory` after Phase 6.4).
- **Queries and subscriptions** begin their tx with
  `{ReadOnly: true, Isolation: REPEATABLE READ}`. The shared `pgMultiDriver`
  (see Phase 8.1) reads those flags and routes the tx to the **reader** pool.
  Postgres treats `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY` as a true
  snapshot, so every statement inside the request observes the same
  point-in-time view.

## Read-your-own-writes caveat

Routing queries to the reader means **read-your-own-writes is best-effort, not
guaranteed**. If the reader is a streaming-replication standby, a separate
GraphQL query issued immediately after a mutation may briefly observe the
pre-mutation state until replication catches up. Within a single request the
guarantee still holds: a mutation's own response is computed inside the
writer tx and therefore reflects the just-committed state.

For UI flows that depend on read-after-write semantics, **return the affected
entity from the mutation itself** (already the standard pattern in this
codebase, e.g. the `Create*Movement` resolvers in `backend/inventory/resolvers`)
rather than issuing a follow-up query. That keeps the post-mutation read
inside the writer tx and avoids any dependency on replica freshness.

## Transaction-ID handle (started-workflow lookup)

Every mutation attempt gets a per-transaction UUID v7 (`txid`), generated at
`BeginTx` and regenerated on each OCC retry. It is stamped on every outbox row
written in the transaction and carried into Temporal as the
`pyck_transaction_id` search attribute of every workflow started from the
mutation's events.

Mutation resolvers expose that ID to clients by assigning it explicitly to
their payload's `transactionID: ID!` field:

```go
return &model.InventoryItemOutput{
    InventoryItem: item,
    TransactionID: gqltx.TransactionID(ctx),
}, nil
```

Because the assignment happens inside the resolver — before the response is
serialized and before the idempotency middleware caches the body — the
committed response is final: nothing mutates it post-commit, and idempotent
replays return the identical body including the handle. A forgotten
assignment fails loudly: `gqltx.TransactionID` returns `uuid.Nil` outside a
transaction, which serializes to `null` and violates the field's non-null
constraint.

Next to the handle, every such payload carries `eventCount: Int!`: how many
outbox events the transaction wrote. Resolvers don't assign it. The events
hook (and any resolver that inserts an outbox row directly, like
`sendCustomEvent`) calls `txid.RecordEvent(ctx)` per row, and the middleware's
`InterceptField` answers the field with `gqltx.EventCount(ctx)`. gqlgen
resolves payload fields after the root resolver returned, so the count is
complete, and it is part of the body the idempotency middleware caches. It is
0 when `FEATURE_SUPPRESS_EVENTS` bypasses the hook: nothing was published, so
there is nothing to route. All root mutation fields of one operation share one
transaction and one counter: each field's `eventCount` is the running total when
that field resolves, so the last field holds the total.

Clients that need to know which workflows a mutation started poll the
workflow service:

```graphql
query { workflowExecutions(where: {transactionID: "<handle>"}) { ... } }
```

An empty connection means "not started or not visible yet" (poll again) or
"the mutation triggered no workflows" — with Temporal's create dedup policy
(`<workflowName>_<entityID>` IDs, duplicates allowed only after failure),
empty-forever is a legitimate outcome for repeat mutations on the same
entity. Poll with a bounded timeout.
