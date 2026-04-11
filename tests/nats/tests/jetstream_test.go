package tests

import (
	"context"
	"fmt"
	"os"
	"time"

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

		It("should successfully request stream info for allowed stream", func() {
			// Request stream info for the default stream
			js := testCtx.JetStream
			Expect(js).NotTo(BeNil())

			stream, err := js.Stream(ctx, config.DefaultStreamName)
			Expect(err).NotTo(HaveOccurred())
			Expect(stream).NotTo(BeNil())

			info, err := stream.Info(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Config.Name).To(Equal(config.DefaultStreamName))
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
			allowedSubject := fmt.Sprintf("%s.%s.events.test", config.DefaultStreamName, testCtx.TenantID)

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
			forbiddenSubject := fmt.Sprintf("%s.%s.crud.workflow.workflow.x.create", config.DefaultStreamName, testCtx.TenantID)

			go func() {
				testCtx.NatsConn.Publish(forbiddenSubject, []byte("crud event message"))
			}()

			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("when publishing to a subject not matching allowed patterns", func() {
		It("should trigger a Permissions Violation", func() {
			invalidSubject := fmt.Sprintf("%s.random.topic", config.DefaultStreamName)
			go func() {
				testCtx.NatsConn.Publish(invalidSubject, []byte("invalid event message"))
			}()

			err := util.WaitForAsyncError(5*time.Second, "Permissions Violation")
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
