# ADR-26W43-01: Run exactly one deployment of the SaaS at a time

> **🟡 Status: Proposed**
>
> 2026-10-23. Records a decision that the release versioning design document ("Versioning and Compatibility", a DRAFT, section 3.2 on page 12) states as made. It is a directional ADR: it fixes the deployment model that the other three release versioning ADRs build on. Related: [ADR-26W43-02](./adr-26w43-02-graphql-stays-v0.md), [ADR-26W43-03](./adr-26w43-03-rollback-by-previous-release-manifest.md) and [ADR-26W43-04](./adr-26w43-04-code-is-the-contract-source-of-truth.md).

## 1 Context

Customers and clients upgrade on their own schedule. The mobile app runs under customer MDM and change control, the `shopify-app` is gated by Shopify review, and customer workers are self-deployed. A customer may hold a version for months (design document, section 3.3, page 13). Something has to keep the old contract working for them while the platform moves on.

One way to do that is to run several deployments side by side: a blue-green deployment per MAJOR version, or parallel deployments that each serve one contract. Each deployment would carry its own data, its own migrations and its own operational load.

The design document rejects that route. Page 12, section 3.2, says: "We run exactly one version of the SaaS/PaaS at a time: no blue-green-by-major, no parallel deployments."

### 1.1 Forces

- Customer-governed consumers cannot be upgraded on demand, so the platform must keep old contracts reachable for a bounded time.
- A parallel deployment is a second copy of the platform, with its own schema state and its own operations. The design document wants maintenance to stay bounded: keep the last K gRPC versions live, "not K whole platforms" (page 19).
- Without a parallel old version, a break has no cushion. The design document accepts that and answers with rarity by design (page 12).
- The compatibility rules the org already follows ([releases.md](https://github.com/pyck-ai/guidelines/blob/main/docs/releases.md#deploy-and-roll-back), [deprecation.md](https://github.com/pyck-ai/guidelines/blob/main/docs/deprecation.md#support-windows)) need one place that decides which contract versions are live.

## 2 Decision

### 2.1 One deployment

The SaaS runs one version of the platform at a time. There is no blue-green deployment per MAJOR version and no parallel deployment of an older version. The rule binds the SaaS only. A self-hosted operator decides for itself how many deployments it runs.

### 2.2 Compatibility lives inside the deployment

Multi-version compatibility comes from API versioning inside the single deployment: one server speaks several contract versions at once. That is the versioned gRPC surface, which the design document plans for later (page 12). Until it exists, the interim policy of page 13 applies: one live contract, additive evolution, and an unavoidable break dark-launched behind a feature toggle.

### 2.3 A break that cannot be versioned away is a flag day

A break that no contract version can absorb is a single, coordinated, pre-announced flag day, executed by switching a feature toggle (page 12). The design document names these cases on page 16: a behaviour change in shared logic, an auth or infrastructure change, retiring a whole version line, and any surface break on `v0`. Keep them rare.

The announcement rules, the support window and the toggle mechanics are owned by [deprecation.md](https://github.com/pyck-ai/guidelines/blob/main/docs/deprecation.md) and [releases.md](https://github.com/pyck-ai/guidelines/blob/main/docs/releases.md#ship-a-behaviour-change). This ADR does not restate them.

## 3 Consequences

### 3.1 Easier

- There is one schema state, one set of data and one operational surface to run and observe. No version skew exists between deployments.
- A release rolls back by redeploying the previous release manifest ([ADR-26W43-03](./adr-26w43-03-rollback-by-previous-release-manifest.md)). That is only a clear operation because there is one deployment to roll back.
- Support cost is bounded by the number of live contract versions, not by the number of platforms.

### 3.2 Harder

- A flag day has no cushion. There is no old deployment to fall back on while consumers catch up, so each such break needs a toggle, an announcement and a moment when every affected consumer is ready.
- All compatibility work moves into the one server. It must serve several gRPC versions at once and keep `v0` working next to them. This work does not exist yet, because gRPC versioning is not built (page 18).
- Behavioural, auth and infrastructure breaks stay flag days even once API versioning exists, because a new contract version absorbs only surface breaks (page 12).
- Customers who hold a version past its end of life are rejected at the gateway. The only help is the platform serving several versions inside one deployment, never a parallel one (page 13).
- The rule does not reach self-hosted installations. A self-hosted operator may run parallel deployments, and then carries the cost of schema, data and operations for each one itself.

### 3.3 Rules out

- Blue-green deployment by MAJOR version.
- A parallel deployment that keeps an old version alive for slow movers, including a per-customer or per-tenant pinned version.
- A per-customer extension of an end-of-life date, because there is no parallel old version to extend (page 13).

## 4 Alternatives considered

The design document names the first two options by rejecting them in one sentence. It does not weigh them in detail, so the reasons below are the ones it gives or directly implies.

### 4.1 Blue-green deployment by MAJOR version

Rejected on page 12. The implied reason (page 12 and page 19) is that this would run whole platforms side by side instead of one narrow, machine-facing surface in several versions. Each deployment multiplies schema, data and operations.

### 4.2 Parallel deployments for slow movers

Rejected on page 12 and again in section 3.3 (page 13): keeping the old contract alive for slow movers "can never mean parallel deployments". The alternative that replaces it is serving the last K gRPC versions in one deployment (page 13 and page 19).

### 4.3 Staged rollout across versions, such as canary

Not an alternative to this decision, and not decided here. Page 25 mentions a canary for write-path drift, and page 10 lists Argo and Kapp as planned deploy tooling, but the design document does not define progressive delivery. Canary and progressive delivery are out of scope for this ADR, which neither allows nor forbids them.
