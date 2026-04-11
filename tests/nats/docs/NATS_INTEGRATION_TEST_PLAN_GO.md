# NATS Integration Test Plan (Go with Ginkgo/Gomega)

## Overview

This document outlines the test plan for NATS integration, focusing on JetStream authentication and authorization. The tests verify that our authentication handler correctly enforces permissions based on tenant IDs and consumer naming conventions. The tests will be written in Go using the `nats-io/nats.go` library, with Ginkgo for BDD-style testing and Gomega for assertions.

## Tech Stack

-   **Testing Framework**: Ginkgo (github.com/onsi/ginkgo/v2)
-   **Assertion Library**: Gomega (github.com/onsi/gomega)
-   **NATS Client**: `nats.go` (github.com/nats-io/nats.go)
-   **Environment Management**: `os.Getenv` (or `joho/godotenv`)
-   **UUID Generation**: `github.com/google/uuid`

## Current Implementation

Our current implementation:

-   Uses a tenant-based authorization model
-   Enforces a specific consumer naming convention (`tenantID--suffix`)
-   Limits each tenant to 10 predefined consumer names (one through ten)
-   Provides specific permissions for JetStream operations
-   Separates publish and subscribe permissions

## Code Style and Structure

-   **BDD Style**: Use Ginkgo's `Describe`, `Context`, `When`, `BeforeEach`, `AfterEach`, and `It` to structure tests.
-   **Descriptive Names**: Use descriptive names for test functions and variables, following Go conventions.
-   **Test Organization**: Group related tests into appropriate test files.
-   **Asynchronous Testing**: Use Ginkgo's `Eventually` and `Consistently` (from Gomega) for asynchronous NATS operations. Use `SpecContext` for timeouts.
-   **Error Handling**: Use Gomega's `Expect(err).To(Succeed())`, `Expect(err).To(HaveOccurred())`, etc.

## Naming Conventions

-   **Variables and Functions**: Use `camelCase` (e.g., `createConsumer`, `validatePermissions`).
-   **Test Functions**: Use `Test` prefix followed by a descriptive name in `PascalCase` (e.g., `TestValidToken`, `TestConsumerCreation`).
-   **Test Files**: Use lowercase and snake_case (e.g., `auth_test.go`, `tenant_permissions_test.go`).

## Go Usage

-   **Standard Library**: Prefer the standard library where possible.
-   **Avoid Global State**: Minimize global variables. Use `BeforeEach` and `AfterEach` for setup/teardown.
-   **Proper Cleanup**: Use `DeferCleanup` to ensure resources are released, even on test failure.
-   **Gomega Matchers**: Leverage Gomega's rich set of matchers for assertions.

## Test Categories

### 1. Authentication Tests

| Test Name     | Description                     | Expected Result                 | Test File          |
| ------------- | ------------------------------- | ------------------------------- | ------------------ |
| TestValidToken   | Connect with a valid JWT token   | Success                         | connection_test.go |
| TestInvalidToken | Connect with an invalid JWT token | Failure (Authentication Error)  | connection_test.go |
| TestMissingToken | Connect without a token         | Failure (Authentication Error)  | connection_test.go |

### 2. Basic Authorization Tests

| Test Name               | Description                                      | Expected Result                 | Test File       |
| ----------------------- | ------------------------------------------------ | ------------------------------- | --------------- |
| TestJetStreamAPIInfo      | Request JetStream API info                        | Success                         | jetstream_test.go |
| TestStreamInfo             | Request stream info for allowed stream            | Success                         | jetstream_test.go |
| TestNonAllowedStreamInfo | Request stream info for non-allowed stream        | Failure (Permission Violation)  | jetstream_test.go |

### 3. Consumer Management Tests

| Test Name                   | Description                                      | Consumer Name          | Stream Name   | Expected Result                 | Test File                   |
| --------------------------- | ------------------------------------------------ | ---------------------- | ------------- | ------------------------------- | --------------------------- |
| TestBasicConsumerCreation     | Create consumer with valid name pattern          | `${tenantId}--one`    | `pyck`        | Success                         | consumer_management_test.go |
| TestInvalidConsumerName       | Create consumer with invalid name pattern        | `invalid-consumer-name` | `pyck`        | Failure (Permission Violation)  | consumer_management_test.go |
| TestNonAllowedStream         | Create consumer for non-allowed stream          | `${tenantId}--one`    | `not-allowed` | Failure (Permission Violation)  | consumer_management_test.go |
| TestDurableConsumerCreation   | Create durable consumer                          | `${tenantId}--two`    | `pyck`        | Success                         | consumer_management_test.go |
| TestConsumerDeletion           | Delete a consumer                                | `${tenantId}--three`  | `pyck`        | Success                         | consumer_management_test.go |
| TestConsumerInfoRequest       | Request consumer info                            | `${tenantId}--four`   | `pyck`        | Success                         | consumer_management_test.go |
| TestConsumerListRequest       | List all consumers                               | N/A                    | `pyck`        | Failure (Permission Violation)  | consumer_management_test.go |
| TestConsumerNamesRequest      | List consumer names                              | N/A                    | `pyck`        | Failure (Permission Violation)  | consumer_management_test.go |
| TestEleventhConsumer           | Create an 11th consumer                         | `${tenantId}--eleven` | `pyck`        | Failure (Permission Violation)  | consumer_management_test.go |
| TestDifferentTenantID         | Access another tenant's resources               | `different-tenant--one` | `pyck`        | Failure (Permission Violation)  | consumer_management_test.go |
| TestDeleteOtherTenantConsumer | Attempt to delete another tenant's consumer    | `different-tenant--one` | `pyck`        | Failure (Permission Violation)  | consumer_management_test.go |

### 4. Message Operation Tests

| Test Name                       | Description                                                            | Consumer Name          | Stream Name   | Expected Result                 | Test File              |
| ------------------------------- | ---------------------------------------------------------------------- | ---------------------- | ------------- | ------------------------------- | ---------------------- |
| TestBasicJetStreamPublishSubscribe | Publish messages to a stream and verify they can be received           | `${tenantId}--five`    | `pyck`        | Success                         | message_operations_test.go |
| TestMessageAcknowledgment         | Verify messages can be properly acknowledged                           | `${tenantId}--six`    | `pyck`        | Success                         | message_operations_test.go |
| TestCrossTenantMessageAccess     | Attempt to access messages from another tenant's subject              | `${tenantId}--seven`  | `pyck`        | Failure (Permission Violation)  | message_operations_test.go |

### 5. Stream Access Tests

| Test Name              | Description                                                | Expected Result                 | Test File         |
| ---------------------- | ---------------------------------------------------------- | ------------------------------- | ----------------- |
| TestStreamAdminOperation | Attempt stream admin operation (create/update/delete)       | Failure (Permission Violation)  | stream_access_test.go |
| TestDirectStreamAccess   | Attempt direct stream access                                | Failure (Permission Violation)  | stream_access_test.go |

### 6. Complex Scenario Tests

| Test Name                   | Description                                      | Consumer Name          | Stream Name   | Expected Result                 | Test File              |
| --------------------------- | ------------------------------------------------ | ---------------------- | ------------- | ------------------------------- | ---------------------- |
| TestFilterSubjectVariation    | Test different filter subject patterns          | `${tenantId}--seven`    | `pyck`        | Success                         | complex_scenarios_test.go |
| TestDeliverSubjectVariation   | Test different deliver subject patterns         | `${tenantId}--eight`   | `pyck`        | Success                         | complex_scenarios_test.go |
| TestMultipleOperationsSequence | Sequence of operations (create, info, delete)   | `${tenantId}--nine`    | `pyck`        | Success                         | complex_scenarios_test.go |
| TestConcurrentOperations       | Multiple concurrent operations                  | Various                | `pyck`        | Success for all valid operations | complex_scenarios_test.go |

## Implementation Details

Tests will be structured using Ginkgo's BDD style, with `Describe`, `Context`, `When`, `BeforeEach`, `AfterEach`, and `It` blocks. Assertions will be made using Gomega matchers. `DeferCleanup` will be used for resource management.

**Strict API Usage:**

-   **JetStream Tests:** All tests interacting with JetStream features (streams, consumers, JetStream publishing/subscribing) **must** use the `jetstream` package (i.e., `jetstream.JetStream`, `jetstream.Consumer`, etc.).  Do *not* use core NATS functions (like `nc.Publish`, `nc.Subscribe`, etc.) for these tests.
-   **Core NATS Tests:** If, and only if, testing *core* NATS functionality (basic connection, non-JetStream pub/sub), use the core NATS functions from the `nats` package (e.g., `nats.Conn`).
-   **Mixing APIs is strictly prohibited.** Any test that mixes `jetstream` and core `nats` functions for the *same* operation is considered invalid. This ensures we are testing the specific functionality of each API layer.

### Helper Functions

Helper functions will be created in a separate file (e.g., `test_helper.go`) to encapsulate common tasks:

-   `createTestConnection(token string) (*nats.Conn, error)`:  Creates a NATS connection with the provided JWT token.
-   `getTenantIDFromJWT(token string) (string, error)`: Extracts the tenant ID from the JWT token.
-   `CreateJetStreamConsumer(ctx context.Context, js jetstream.JetStream, streamName, consumerName string) (jetstream.Consumer, error)`: Creates a JetStream consumer.
-   `deleteJetStreamConsumer(ctx context.Context, js jetstream.JetStream, streamName, consumerName string) error`: Deletes a JetStream consumer.
-   `createCoreNATSSubscription(nc *nats.Conn, subject string) (*nats.Subscription, error)`: Creates a core NATS subscription (only if needed for specific core NATS tests).

### Test Files Organization

Organize tests into the following files:

1.  `connection_test.go` - Authentication tests.
2.  `jetstream_test.go` - Basic JetStream operations and permissions.
3.  `consumer_management_test.go` - Consumer creation, deletion, and management.
4.  `message_operations_test.go` - Message publishing, retrieval, and acknowledgment.
5.  `stream_access_test.go` - Stream access restrictions.
6.  `complex_scenarios_test.go` - Complex scenarios and edge cases.
7.  `core_nats_test.go` - *Only if needed*: Tests for core NATS functionality (if any).

## Execution Environment

-   Tests run using the `ginkgo` command (or `go test` with appropriate flags).
-   Environment variables can be loaded using `os.Getenv` or a library like `joho/godotenv`.
-   Use `SpecContext` to set timeouts for individual operations within tests.
-   Tests should be isolated to prevent interference. Use unique tenant IDs and consumer names for each test. Ginkgo's parallel execution capabilities will be utilized.

## Implementation Status

| Test File                   | Status        | Notes                                         |
| --------------------------- | ------------- | --------------------------------------------- |
| connection_test.go          | Not Started   |                                               |
| jetstream_test.go           | Not Started   |                                               |
| consumer_management_test.go | Not Started   |                                               |
| message_operations_test.go  | Not Started   |                                               |
| stream_access_test.go       | Not Started   |                                               |
| complex_scenarios_test.go   | Not Started   |                                               |
| core_nats_test.go           | Not Started   | Only if core NATS tests are required.         |

## NATS Testing Best Practices (Go with Ginkgo/Gomega)

-   **Connection Management**: Create and close NATS connections properly for each test or test suite. Use `BeforeEach`, `AfterEach`, and `DeferCleanup`.
-   **JetStream API**: Use the `jetstream` package for all JetStream operations.
-   **Consumer Naming**: Ensure consumer names follow the pattern `$tenant_id--$suffix` in all tests.
-   **Permission Testing**: Test both positive and negative permission scenarios thoroughly.
-   **Error Handling**: Check for errors after every NATS operation. Use Gomega matchers for assertions.
-   **Context Usage**: Use `SpecContext` for managing timeouts and cancellation.
-   **Asynchronous Operations**: Use `Eventually` and `Consistently` for asynchronous operations.
-   **Parallel Execution**: Utilize Ginkgo's parallel execution features (`ginkgo -p`).
- **Strict API Separation**:  Never mix `nats` and `jetstream` functions within the same test for the same operation.

## Future Enhancements

Consider adding these enhancements to the test suite:

1.  Performance testing for high-throughput scenarios.
2.  Chaos testing (network disruptions, server restarts).
3.  Long-running stability tests.
4.  Cross-tenant isolation verification.
5.  Token rotation and expiration handling. 