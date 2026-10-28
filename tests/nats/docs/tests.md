# NATS Integration Test Patterns

## Test Cases and Patterns

| Test Case | Stream Name | Topic (Subject) | Consumer Name |
|-----------|-------------|-----------------|---------------|
| Create consumer with valid pattern | `pyck-$tenant_id` | `pyck.$tenant_id.>` | `$tenant_id--one` |
| Create consumer with valid pattern (star) | `pyck-$tenant_id` | `pyck.$tenant_id.*` | `$tenant_id--one` |
| Create consumer with invalid name | `pyck-$tenant_id` | `pyck.$tenant_id.>` | `invalid-consumer-name` |
| Create consumer with invalid subject | `pyck-$tenant_id` | `wrong.$tenant_id.>` | `$tenant_id--one` |
| Create consumer with missing wildcard | `pyck-$tenant_id` | `pyck.$tenant_id` | `$tenant_id--one` |
| Create consumer with wrong wildcard position | `pyck-$tenant_id` | `pyck.>.tenant_id` | `$tenant_id--one` |
| Create consumer with invalid wildcard format | `pyck-$tenant_id` | `pyck.$tenant_id.*>` | `$tenant_id--one` |
| Create consumer for non-allowed stream | `non-allowed-stream` | `pyck.$tenant_id.>` | `$tenant_id--one` |
| Create and delete durable consumer | `pyck-$tenant_id` | `pyck.$tenant_id.>` | `$tenant_id--two` |
| Request consumer info | `pyck-$tenant_id` | `pyck.$tenant_id.>` | `$tenant_id--three` |
| List all consumers | `pyck-$tenant_id` | `$JS.API.CONSUMER.LIST.pyck-$tenant_id` | N/A |
| List consumer names | `pyck-$tenant_id` | `$JS.API.CONSUMER.NAMES.pyck-$tenant_id` | N/A |
| Create 11th consumer (limit test) | `pyck-$tenant_id` | `pyck.$tenant_id.>` | `$tenant_id--one` through<br>`$tenant_id--eleven` |
| Access another tenant's consumer | `pyck-$tenant_id` | `pyck.different-tenant.>` | `different-tenant--one` |
| Delete another tenant's consumer | `pyck-$tenant_id` | N/A | `different-tenant--one` |

## Key Rules

1. **Stream Name**
   - Only allowed stream is the tenant's own, `pyck-$tenant_id`, which holds the
     subjects `pyck.$tenant_id.>`; the services' shared stream `pyck`, the system
     stream `pyck-system` and every other tenant's stream are denied, and so is
     listing stream names

2. **Topic Pattern**
   - Valid format: `pyck.$tenant_id.>` or `pyck.$tenant_id.*`
   - Wildcards (`>` or `*`) are required at the end of the subject
   - Must follow one of the two valid patterns exactly

3. **Consumer Name Pattern**
   - Valid format: `$tenant_id--[one|two|...|ten]`
   - Maximum of 10 consumers per tenant

4. **Tenant ID**
   - The tenant UUID from the `X-Pyck-Tenant-Id` connect header, or the user's only tenant when the header is absent or `all`
   - Subjects, consumer names and the reply inbox use its canonical lower-case form

5. **Reply Inbox**
   - Subscribe is granted on nothing but `_INBOX.<tenantUUID>.>`: no `_INBOX.>`, and no core subscription to the tenant's own subjects
   - Clients must connect with `nats.CustomInboxPrefix("_INBOX.<tenantUUID>")`
   - On the default prefix the connection succeeds, but every request, JetStream call and fetch times out
