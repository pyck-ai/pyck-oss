# NATS Integration Test Patterns

## Test Cases and Patterns

| Test Case | Stream Name | Topic (Subject) | Consumer Name |
|-----------|-------------|-----------------|---------------|
| Create consumer with valid pattern | `pyck` | `pyck.$tenant_id.>` | `$tenant_id--one` |
| Create consumer with valid pattern (star) | `pyck` | `pyck.$tenant_id.*` | `$tenant_id--one` |
| Create consumer with invalid name | `pyck` | `pyck.$tenant_id.>` | `invalid-consumer-name` |
| Create consumer with invalid subject | `pyck` | `wrong.$tenant_id.>` | `$tenant_id--one` |
| Create consumer with missing wildcard | `pyck` | `pyck.$tenant_id` | `$tenant_id--one` |
| Create consumer with wrong wildcard position | `pyck` | `pyck.>.tenant_id` | `$tenant_id--one` |
| Create consumer with invalid wildcard format | `pyck` | `pyck.$tenant_id.*>` | `$tenant_id--one` |
| Create consumer for non-allowed stream | `non-allowed-stream` | `pyck.$tenant_id.>` | `$tenant_id--one` |
| Create and delete durable consumer | `pyck` | `pyck.$tenant_id.>` | `$tenant_id--two` |
| Request consumer info | `pyck` | `pyck.$tenant_id.>` | `$tenant_id--three` |
| List all consumers | `pyck` | `$JS.API.CONSUMER.LIST.pyck` | N/A |
| List consumer names | `pyck` | `$JS.API.CONSUMER.NAMES.pyck` | N/A |
| Create 11th consumer (limit test) | `pyck` | `pyck.$tenant_id.>` | `$tenant_id--one` through<br>`$tenant_id--eleven` |
| Access another tenant's consumer | `pyck` | `pyck.different-tenant.>` | `different-tenant--one` |
| Delete another tenant's consumer | `pyck` | N/A | `different-tenant--one` |

## Key Rules

1. **Stream Name**
   - Only allowed stream name is `pyck`

2. **Topic Pattern**
   - Valid format: `pyck.$tenant_id.>` or `pyck.$tenant_id.*`
   - Wildcards (`>` or `*`) are required at the end of the subject
   - Must follow one of the two valid patterns exactly

3. **Consumer Name Pattern**
   - Valid format: `$tenant_id--[one|two|...|ten]`
   - Maximum of 10 consumers per tenant
   - Must use numeric tenant ID (not UUID)

4. **Tenant ID**
   - Extracted from JWT token claim: `urn:zitadel:iam:user:resourceowner:id`
   - Must be the numeric ID, not converted to UUID