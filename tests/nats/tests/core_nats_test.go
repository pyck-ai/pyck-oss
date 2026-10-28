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

var _ = Describe("Core NATS Operations", func() {
	var (
		testCtx *util.TestContext
		err     error
	)

	BeforeEach(func() {
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

	// Tenant clients share one account, where the answers to a request are
	// delivered on whatever reply subject it names, other tenants' subjects
	// included; a tenant reads its events through its consumers instead.
	Context("Core NATS subscription", func() {
		It("should be refused a core subscription to its own tenant's subjects", func() {
			subject := fmt.Sprintf("%s.%s.>", config.StreamName(), testCtx.TenantID)

			select {
			case err := <-util.AsyncErrorChannel:
				GinkgoWriter.Printf("discarding an earlier async error: %v\n", err)
			default:
			}

			_, err := util.CreateCoreNATSSubscription(testCtx.NatsConn, subject)
			Expect(err).NotTo(HaveOccurred())
			Expect(testCtx.NatsConn.Flush()).To(Succeed())

			err = util.WaitForAsyncError(5*time.Second, "Permissions Violation for Subscription")
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("Publishing on custom events within my tenant", func() {
		It("should store a custom event in the tenant's own stream", func() {
			subject := fmt.Sprintf("%s.%s.events.test", config.StreamName(), testCtx.TenantID)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			ack, err := testCtx.JetStream.Publish(ctx, subject, []byte("custom event message"))
			Expect(err).NotTo(HaveOccurred())
			Expect(ack.Stream).To(Equal(util.TenantStreamName(testCtx.TenantID)))
		})
	})
})
