package tests

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pyck-ai/pyck/tests/nats/config"
	"github.com/pyck-ai/pyck/tests/nats/util"
)

var _ = Describe("JetStream Operations", func() {
	var (
		testCtx *util.TestContext
		ctx     context.Context
		err     error
	)

	BeforeEach(func() {
		ctx = context.Background()
		token := os.Getenv(config.EnvAuthJWTToken)
		Expect(token).NotTo(BeEmpty(), "PYCK_TEST_AUTH_TOKEN environment variable must be set")

		tenantID := os.Getenv(config.EnvTenantID)
		testCtx, err = util.NewTestContext(token, tenantID)
		Expect(err).NotTo(HaveOccurred())

		err = testCtx.Setup()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if testCtx != nil {
			testCtx.Cleanup()
		}
	})

	Context("with JetStream API", func() {
		It("should successfully request JetStream API info", func() {
			// Request JetStream API info with a longer timeout
			jsInfo, err := testCtx.NatsConn.Request("$JS.API.INFO", nil, 30*time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(jsInfo).NotTo(BeNil())
			Expect(jsInfo.Data).NotTo(BeEmpty())
		})

		It("should successfully request stream info for the tenant's own stream", func() {
			js := testCtx.JetStream
			Expect(js).NotTo(BeNil())

			stream, err := js.Stream(ctx, util.TenantStreamName(testCtx.TenantID))
			Expect(err).NotTo(HaveOccurred())
			Expect(stream).NotTo(BeNil())

			info, err := stream.Info(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Config.Name).To(Equal(util.TenantStreamName(testCtx.TenantID)))
			Expect(info.Config.Subjects).To(Equal([]string{fmt.Sprintf("%s.%s.>", config.StreamName(), testCtx.TenantID)}))
		})

		It("should fail to request stream info for the services' shared stream", func() {
			subject := fmt.Sprintf("$JS.API.STREAM.INFO.%s", config.StreamName())

			Expect(testCtx.NatsConn.Publish(subject, nil)).To(Succeed())

			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should fail to list stream names, which carry tenant IDs", func() {
			Expect(testCtx.NatsConn.Publish("$JS.API.STREAM.NAMES", nil)).To(Succeed())

			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should fail to request stream info for non-allowed stream", func() {
			// Try to request info for a non-allowed stream directly
			nonAllowedStream := "non-allowed-stream"
			subject := fmt.Sprintf("$JS.API.STREAM.INFO.%s", nonAllowedStream)

			// Send the request and wait for the async error
			go func() {
				testCtx.NatsConn.Request(subject, nil, 5*time.Second)
			}()

			// Wait for the permissions violation in the async error handler
			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when publishing to allowed custom event topics", func() {
		It("should publish successfully without triggering a Permissions Violation", func() {
			allowedSubject := fmt.Sprintf("%s.%s.events.test", config.StreamName(), testCtx.TenantID)

			testCtx.NatsConn.Publish(allowedSubject, []byte("custom event message"))
			time.Sleep(1 * time.Second)

			select {
			case asyncErr := <-util.AsyncErrorChannel:
				Fail(fmt.Sprintf("unexpected async error: %v", asyncErr))
			default:
			}
		})
	})

	Context("when publishing to CRUD event topics", func() {
		It("should fail to publish to CRUD topics and trigger a Permissions Violation", func() {
			forbiddenSubject := fmt.Sprintf("%s.%s.crud.workflow.workflow.x.create", config.StreamName(), testCtx.TenantID)

			go func() {
				testCtx.NatsConn.Publish(forbiddenSubject, []byte("crud event message"))
			}()

			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when publishing a custom event directly", func() {
		It("should be refused: custom events go through sendCustomEvent", func() {
			forbiddenSubject := fmt.Sprintf("%s.%s.custom.tour-creation.%s.create", config.StreamName(), testCtx.TenantID, uuid.NewString())

			Expect(testCtx.NatsConn.Publish(forbiddenSubject, []byte("custom event message"))).To(Succeed())

			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when publishing to a subject not matching allowed patterns", func() {
		It("should trigger a Permissions Violation", func() {
			invalidSubject := fmt.Sprintf("%s.random.topic", config.StreamName())
			go func() {
				testCtx.NatsConn.Publish(invalidSubject, []byte("invalid event message"))
			}()

			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
