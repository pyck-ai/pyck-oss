package tests

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pyck-ai/pyck/tests/nats/config"
	"github.com/pyck-ai/pyck/tests/nats/util"
)

var _ = Describe("Consumer Management", func() {
	var (
		testCtx *util.TestContext
		cancel  context.CancelFunc
		ctx     context.Context
	)

	BeforeEach(func() {
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)

		token := os.Getenv(config.EnvAuthJWTToken)
		Expect(token).NotTo(BeEmpty(), "PYCK_TEST_AUTH_TOKEN environment variable must be set")

		tenantID := os.Getenv("PYCK_TEST_TENANT_ID")

		var err error
		testCtx, err = util.NewTestContext(token, tenantID)
		Expect(err).NotTo(HaveOccurred())

		util.LogInfo("Using tenant ID: %s", testCtx.TenantID)

		err = testCtx.Setup()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if testCtx != nil {
			testCtx.Cleanup()
		}

		defer cancel()
	})

	Context("with valid stream", func() {
		It("should successfully create a consumer with valid name pattern and subject", func() {
			consumerName := fmt.Sprintf("%s--one", testCtx.TenantID)
			subject := fmt.Sprintf("pyck.%s.>", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(consumer).NotTo(BeNil(), "Consumer should not be nil")
			Expect(testCtx.NatsConn.Status()).To(Equal(nats.CONNECTED), "NATS connection should be active")

			// Verify consumer exists
			info, err := consumer.Info(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Config.Name).To(Equal(consumerName))
			Expect(info.Config.FilterSubject).To(Equal(subject))
		})

		It("should successfully create a consumer with star wildcard", func() {
			consumerName := fmt.Sprintf("%s--one", testCtx.TenantID)
			subject := fmt.Sprintf("pyck.%s.*", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(consumer).NotTo(BeNil())

			// Verify consumer exists
			info, err := consumer.Info(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Config.Name).To(Equal(consumerName))
			Expect(info.Config.FilterSubject).To(Equal(subject))
		})

		It("should fail to create a consumer with invalid name pattern", func() {
			invalidName := "invalid-consumer-name"
			subject := fmt.Sprintf("pyck.%s.>", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, invalidName, subject)
			Expect(err).To(HaveOccurred())
			Expect(consumer).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("Permissions Violation"))
		})

		It("should fail to create a consumer with invalid subject pattern", func() {
			consumerName := fmt.Sprintf("%s--one", testCtx.TenantID)
			invalidSubject := fmt.Sprintf("wrong.%s.>", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, invalidSubject)
			Expect(err).To(HaveOccurred())
			Expect(consumer).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("Permissions Violation"))
		})

		It("should fail to create a consumer with missing wildcard", func() {
			consumerName := fmt.Sprintf("%s--one", testCtx.TenantID)
			invalidSubject := fmt.Sprintf("pyck.%s", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, invalidSubject)
			Expect(err).To(HaveOccurred())
			Expect(consumer).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("Permissions Violation"))
		})

		It("should fail to create a consumer with wrong wildcard position", func() {
			consumerName := fmt.Sprintf("%s--one", testCtx.TenantID)
			invalidSubject := fmt.Sprintf("pyck.>.%s", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, invalidSubject)
			Expect(err).To(HaveOccurred())
			Expect(consumer).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("Permissions Violation"))
		})

		It("should fail to create a consumer for non-allowed stream", func() {
			consumerName := fmt.Sprintf("%s--one", testCtx.TenantID)
			subject := fmt.Sprintf("pyck.%s.>", testCtx.TenantID)
			nonAllowedStream := "non-allowed-stream"

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, nonAllowedStream, consumerName, subject)
			Expect(err).To(HaveOccurred())
			Expect(consumer).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("Permissions Violation"))
		})

		It("should successfully create and delete a durable consumer", func() {
			consumerName := fmt.Sprintf("%s--two", testCtx.TenantID)
			subject := fmt.Sprintf("pyck.%s.>", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(consumer).NotTo(BeNil())

			// Verify consumer exists and is durable
			info, err := consumer.Info(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Config.Name).To(Equal(consumerName))
			Expect(info.Config.Durable).To(Equal(consumerName))
			Expect(info.Config.FilterSubject).To(Equal(subject))

			// Delete the consumer
			err = util.DeleteJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName)
			Expect(err).NotTo(HaveOccurred())

			// Verify consumer no longer exists
			_, err = consumer.Info(ctx)
			Expect(err).To(HaveOccurred())
		})

		It("should successfully request consumer info", func() {
			consumerName := fmt.Sprintf("%s--three", testCtx.TenantID)
			subject := fmt.Sprintf("pyck.%s.>", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(consumer).NotTo(BeNil())

			// Request consumer info
			info, err := consumer.Info(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Config.Name).To(Equal(consumerName))
			Expect(info.Stream).To(Equal(config.DefaultStreamName))
			Expect(info.Config.FilterSubject).To(Equal(subject))
		})

		It("should fail to list all consumers", func() {
			// Try to list all consumers directly using NATS API
			subject := fmt.Sprintf("$JS.API.CONSUMER.LIST.%s", config.DefaultStreamName)

			// Send the request and wait for the async error
			go func() {
				testCtx.NatsConn.Request(subject, nil, 5*time.Second)
			}()

			// Wait for the permissions violation in the async error handler
			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should fail to list consumer names", func() {
			// Try to list consumer names directly using NATS API
			subject := fmt.Sprintf("$JS.API.CONSUMER.NAMES.%s", config.DefaultStreamName)

			// Send the request and wait for the async error
			go func() {
				testCtx.NatsConn.Request(subject, nil, 5*time.Second)
			}()

			// Wait for the permissions violation in the async error handler
			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("with consumer limits", func() {
		It("should fail to create an eleventh consumer", func() {
			// Create 10 consumers
			consumerSuffixes := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
			for i := 1; i <= 10; i++ {
				consumerName := fmt.Sprintf("%s--%s", testCtx.TenantID, consumerSuffixes[i-1])
				subject := fmt.Sprintf("pyck.%s.>", testCtx.TenantID)

				consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, subject)
				Expect(err).NotTo(HaveOccurred())
				Expect(consumer).NotTo(BeNil())
			}

			// Try to create the 11th consumer
			consumerName := fmt.Sprintf("%s--eleven", testCtx.TenantID)
			subject := fmt.Sprintf("pyck.%s.>", testCtx.TenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, subject)
			Expect(err).To(HaveOccurred())
			Expect(consumer).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("Permissions Violation"))
		})
	})

	Context("with tenant isolation", func() {
		It("should fail to access another tenant's consumer", func() {
			otherTenantID := "different-tenant"
			consumerName := fmt.Sprintf("%s--one", otherTenantID)
			subject := fmt.Sprintf("pyck.%s.>", otherTenantID)

			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName, subject)
			Expect(err).To(HaveOccurred())
			Expect(consumer).To(BeNil())
			Expect(err.Error()).To(ContainSubstring("Permissions Violation"))
		})

		It("should fail to delete another tenant's consumer", func() {
			otherTenantID := "different-tenant"
			consumerName := fmt.Sprintf("%s--one", otherTenantID)

			err := util.DeleteJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, config.DefaultStreamName, consumerName)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Permissions Violation"))
		})
	})
})
