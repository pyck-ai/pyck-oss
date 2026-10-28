# ADR-26W43-02: GraphQL stays `v0` forever, hard versioning is gRPC

> **🟡 Status: Proposed**
>
> 2026-10-23. Records a decision that the release versioning design document ("Versioning and Compatibility", a DRAFT, section 3.5 on pages 16 and 17) states as made: "we have decided not to invest" in parallel-versioned GraphQL. It is a directional ADR. gRPC does not exist yet, so the second half of the decision describes an end state. Related: [ADR-26W43-01](./adr-26w43-01-single-deployment-of-the-saas.md).

## 1 Context

The GraphQL API is the only API today. UIs use it directly. Workers and integrations use it through the SDKs (`pyck-sdk`, `workflowsdk`), which wrap it (page 18). Nothing is versioned: everyone is on `v0`, which has no compatibility guarantee (page 12).

Consumers differ in how much they can absorb:

- The UIs and any direct GraphQL integration release in lockstep with the API and tolerate additive change (page 17, page 19).
- Customer-governed workers, the `shopify-app` and the mobile app cannot be upgraded on demand and need versioned, deprecation-managed contracts (page 13, page 19).

Parallel-versioned GraphQL schemas would need one of two things. The first is a projection layer between ent and the GraphQL boundary. The second is a fork of the ent, entgql and gqlgen pipeline, because the supergraph is derived from a single ent database schema (pages 16 and 17). The design document also gives a reason of kind: GraphQL is "bad at hard versioning". Federation composes subgraphs, and `@deprecated` covers additive deprecation only, not semantic breaks (page 18).

### 1.1 Forces

- Customer-governed consumers need hard versioning (page 13).
- The ent to entgql to gqlgen pipeline derives everything from one schema. Versioning it is expensive (page 16 and 17).
- A versioned surface for the machine-facing worker path is cheap to add in gRPC, where parallel versions are explicit packages (`pyck.workflow.v1`, `.v2`; page 18).
- Two governance models next to each other are a cost. The design document calls the trade-off intentional (page 17).

## 2 Decision

### 2.1 GraphQL is `v0` only

GraphQL is `v0`, unversioned, and stays that way. Its contract is bound to the component version of `pyck`, and consumers release in lockstep with it. GraphQL persists as a permanent surface, not on a removal schedule (page 16, page 18). We do not build parallel-versioned GraphQL schemas.

### 2.2 Consumers that need hard versioning use gRPC

An audience that needs hard versioning uses gRPC instead. gRPC versions (`v1`, `v2`, ...) run a lifecycle from `alpha` through `beta`, `ga` and `legacy` to `removed`. A breaking surface change ships as a new version beside the old one (page 17).

### 2.3 Two surfaces, split by audience

| | gRPC | GraphQL |
| --- | --- | --- |
| Version | `v1`, `v2`, ... | `v0` |
| Audience | worker path: `workflowsdk`, customer workers | UI path: web, mobile, direct integrations |
| Breaking change | new version beside the old | any release, announced; consumers coordinate |
| Guarantee | per version, within a deprecation window | none on a break; additive by default |

This table summarises page 18. The rules that govern each column are owned elsewhere: [deprecation.md](https://github.com/pyck-ai/guidelines/blob/main/docs/deprecation.md#break-v0-with-an-announcement) for breaking `v0` and for the gRPC lifecycle, and [releases.md](https://github.com/pyck-ai/guidelines/blob/main/docs/releases.md#ship-a-behaviour-change) for the toggle. This ADR does not restate them.

### 2.4 What the decision does not decide

The design document marks gRPC as end state: "gRPC does not exist yet" (page 18). The decision to keep GraphQL at `v0` holds today. The decision to send hard-versioning audiences to gRPC is a commitment to build that surface, not a description of something running.

## 3 Consequences

### 3.1 Easier

- The ent, entgql and gqlgen pipeline stays as it is. No projection layer and no fork.
- `v0` consumers keep one schema and one governance model for their path.
- Hard versioning is concentrated on a narrow, machine-facing surface, so keeping the last K versions live is cheap (page 19).

### 3.2 Harder

- Two governance models run in parallel: best-effort additive evolution on `v0`, formal guarantees on gRPC. The design document calls this intentional (page 17).
- A `v0` break can land in any release. The mitigations are the schema-diff gate, additive-by-default, the `@deprecated` policy and `X-Pyck-Client` telemetry (page 17). Of these, only the `graphql-inspector` schema diff is in trial use today (pages 2 and 24).
- Any direct GraphQL integration that needs a guarantee must move to gRPC or accept lockstep.
- The SDK's switch from GraphQL to gRPC is a hard break that ships as a new major SDK version, with no transparent cutover. The legacy GraphQL SDK is retired when its traffic falls below an operational threshold, not on a date (page 18).
- Until gRPC exists, nobody can use versioned contracts, and `v0` plus feature toggles hold everything together (page 12, page 24).

### 3.3 Rules out

- Parallel-versioned GraphQL schemas, such as `/graphql/v1` beside `/graphql/v2`.
- A projection layer between ent and the GraphQL boundary, built for versioning.
- A removal schedule for GraphQL `v0`.
- Giving a customer a frozen GraphQL contract. The answer to such a request is gRPC.

## 4 Alternatives considered

The design document names the two implementations of parallel GraphQL versions and rejects both in one sentence (page 16 and 17). It does not discuss other options. Section 4.3 is implied by the text and is not named as an option.

### 4.1 A projection layer between ent and the GraphQL boundary

Rejected: "we have decided not to invest in either" (page 17). The layer would let several GraphQL versions project from one ent schema, at the cost of building and maintaining the projection.

### 4.2 Fork the ent, entgql and gqlgen pipeline

Rejected for the same reason and in the same sentence (page 17). The supergraph is derived from a single ent database schema, so a fork would carry one pipeline per version.

### 4.3 Version GraphQL with `@deprecated` alone

Implied by the page 18 argument. `@deprecated` covers additive deprecation but not semantic breaks, and federation composes subgraphs, so it does not give hard versioning. The design document uses `@deprecated` as a mitigation on `v0` and not as a versioning scheme.
