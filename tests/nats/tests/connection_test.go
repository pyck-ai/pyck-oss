package tests

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pyck-ai/pyck/tests/nats/util"
)

var _ = Describe("NATS Connection", func() {
	var testCtx *util.TestContext

	BeforeEach(func() {
		// Get the token from environment variable
		token := os.Getenv("PYCK_TEST_AUTH_TOKEN")
		Expect(token).NotTo(BeEmpty(), "PYCK_TEST_AUTH_TOKEN environment variable must be set")

		tenantID := os.Getenv("PYCK_TENANT_ID")

		var err error
		testCtx, err = util.NewTestContext(token, tenantID)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if testCtx != nil {
			testCtx.Cleanup()
		}
	})

	Context("with authentication", func() {
		It("should successfully connect with a valid token", func() {
			err := testCtx.Setup()
			Expect(err).NotTo(HaveOccurred())
			Expect(testCtx.NatsConn).NotTo(BeNil())
			Expect(testCtx.NatsConn.IsConnected()).To(BeTrue())
			Expect(testCtx.TenantID).NotTo(BeEmpty())
		})

		It("should fail to connect with an invalid token", func() {
			invalidCtx, err := util.NewTestContext("invalid.token.here", "")
			Expect(err).To(HaveOccurred())
			Expect(invalidCtx).To(BeNil())
		})

		It("should fail to connect with an empty token", func() {
			emptyCtx, err := util.NewTestContext("", "")
			Expect(err).To(HaveOccurred())
			Expect(emptyCtx).To(BeNil())
		})
	})
})
