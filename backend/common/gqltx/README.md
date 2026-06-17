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
