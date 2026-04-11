package tests

import (
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

	Context("Core NATS subscription", func() {
		It("should subscribe to every event in my tenant and receive published messages", func() {
			subject := fmt.Sprintf("%s.%s.>", config.DefaultStreamName, testCtx.TenantID)
			subscription, err := util.CreateCoreNATSSubscription(testCtx.NatsConn, subject)
			Expect(err).NotTo(HaveOccurred())

			testMessage := []byte("tenant event message")
			err = testCtx.NatsConn.Publish(subject, testMessage)
			Expect(err).NotTo(HaveOccurred())

			receivedMsg, err := subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(receivedMsg.Data).To(Equal(testMessage))
		})
	})

	Context("Publishing on custom events within my tenant", func() {
		It("should successfully publish and receive custom event messages", func() {
			subject := fmt.Sprintf("%s.%s.events.test", config.DefaultStreamName, testCtx.TenantID)

			subscription, err := util.CreateCoreNATSSubscription(testCtx.NatsConn, subject)
			Expect(err).NotTo(HaveOccurred())

			testMessage := []byte("custom event message")
			err = testCtx.NatsConn.Publish(subject, testMessage)
			Expect(err).NotTo(HaveOccurred())

			receivedMsg, err := subscription.NextMsg(5 * time.Second)
			Expect(err).NotTo(HaveOccurred())
			Expect(receivedMsg.Data).To(Equal(testMessage))
		})
	})
})
