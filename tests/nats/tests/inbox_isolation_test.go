package tests_test

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/pyck-ai/pyck/tests/nats/config"
	"github.com/pyck-ai/pyck/tests/nats/util"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Every tenant client shares one account, and with it one inbox space.
// Replies, JetStream fetch batches carrying stream payloads included, travel
// on inbox subjects, so a tenant able to subscribe beyond its own inbox
// prefix reads other tenants' replies. The tenant's own replies reach it
// through the account's imports of the JetStream API.
var _ = Describe("Reply inbox isolation", func() {
	var (
		testCtx        *util.TestContext
		probeConsumers []string
	)

	BeforeEach(func() {
		probeConsumers = nil

		token := os.Getenv(config.EnvAuthJWTToken)
		Expect(token).NotTo(BeEmpty(), "PYCK_TEST_AUTH_TOKEN environment variable must be set")

		var err error
		testCtx, err = util.NewTestContext(token, os.Getenv(config.EnvTenantID))
		Expect(err).NotTo(HaveOccurred())
		Expect(testCtx.Setup()).To(Succeed())
	})

	AfterEach(func(ctx SpecContext) {
		if testCtx == nil {
			return
		}
		// Before Cleanup: the delete needs the connection Cleanup closes.
		for _, name := range probeConsumers {
			if err := testCtx.JetStream.DeleteConsumer(ctx, util.TenantStreamName(testCtx.TenantID), name); err != nil {
				util.LogInfo("could not delete probe consumer %s: %v", name, err)
			}
		}
		testCtx.Cleanup()
	}, NodeTimeout(10*time.Second))

	DescribeTable("refuses a subscription beyond the tenant's own inbox prefix",
		func(subject func(tenantID string) string, queue string) {
			subj := subject(testCtx.TenantID)
			// Only errors raised by this subscription may satisfy the wait.
			for len(util.AsyncErrorChannel) > 0 {
				err := <-util.AsyncErrorChannel
				GinkgoWriter.Printf("discarding an earlier async error: %v\n", err)
			}

			// The client registers the subscription locally; the server
			// refuses it asynchronously with a permissions violation.
			var (
				sub *nats.Subscription
				err error
			)
			if queue == "" {
				sub, err = testCtx.NatsConn.SubscribeSync(subj)
			} else {
				sub, err = testCtx.NatsConn.QueueSubscribeSync(subj, queue)
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(testCtx.NatsConn.Flush()).To(Succeed())

			Expect(util.WaitForAsyncError(5*time.Second,
				fmt.Sprintf("Permissions Violation for Subscription to %q", subj))).To(Succeed())
			Expect(sub.Unsubscribe()).To(Succeed())
		},
		Entry("the whole inbox space", fixed("_INBOX.>"), ""),
		Entry("another tenant's inbox prefix", fixed("_INBOX."+uuid.NewString()+".>"), ""),
		// A wildcard in the tenant token is not a subset of the literal
		// grant, although it would cover the tenant's own inboxes too.
		Entry("every tenant's inboxes through a token wildcard", fixed("_INBOX.*.>"), ""),
		Entry("every connection's request inboxes", fixed("_INBOX.*.*.*"), ""),
		Entry("every default-prefix inbox", fixed("_INBOX.*"), ""),
		Entry("the whole subject space", fixed(">"), ""),
		Entry("every subject under any first token", fixed("*.>"), ""),
		Entry("the whole inbox space as a queue group", fixed("_INBOX.>"), "inbox-probe"),
		// Subjects are case-sensitive and the grant names the canonical,
		// lower-case id: a client spelling its tenant in upper case is not
		// on its prefix.
		Entry("the tenant's own id in upper case", func(tenantID string) string {
			return "_INBOX." + strings.ToUpper(tenantID) + ".>"
		}, ""),
	)

	It("acks asynchronous publishes on the tenant's inbox prefix, in both JetStream APIs", func(ctx SpecContext) {
		subject := fmt.Sprintf("%s.%s.events.inboxprobe.%s", util.TenantStreamName(testCtx.TenantID), testCtx.TenantID, uuid.NewString())

		// The jetstream package acks on its own reply prefix, not on the
		// request mux of a synchronous Publish.
		future, err := testCtx.JetStream.PublishAsync(subject, []byte("async probe"))
		Expect(err).NotTo(HaveOccurred())
		Eventually(future.Ok()).WithTimeout(10 * time.Second).Should(Receive())

		// The legacy nats.JetStreamContext builds its reply prefix separately.
		legacy, err := testCtx.NatsConn.JetStream()
		Expect(err).NotTo(HaveOccurred())
		legacyFuture, err := legacy.PublishAsync(subject, []byte("legacy async probe"))
		Expect(err).NotTo(HaveOccurred())
		Eventually(legacyFuture.Ok()).WithTimeout(10 * time.Second).Should(Receive())

		// A plain JetStream API request travels through the request mux.
		_, err = testCtx.JetStream.AccountInfo(ctx)
		Expect(err).NotTo(HaveOccurred())
	}, SpecTimeout(30*time.Second))

	It("delivers Consume and Messages pulls on the tenant's inbox prefix", func(ctx SpecContext) {
		// probe publishes payload on a subject of its own and creates the
		// consumer slot suffix filtered on it, so each pull loop below sees
		// exactly its own message.
		probe := func(suffix string, payload []byte) jetstream.Consumer {
			subject := fmt.Sprintf("%s.%s.events.inboxprobe.%s", util.TenantStreamName(testCtx.TenantID), testCtx.TenantID, uuid.NewString())
			_, err := testCtx.JetStream.Publish(ctx, subject, payload)
			Expect(err).NotTo(HaveOccurred())

			name := fmt.Sprintf("%s--%s", testCtx.TenantID, suffix)
			probeConsumers = append(probeConsumers, name)
			consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, util.TenantStreamName(testCtx.TenantID), name, subject)
			Expect(err).NotTo(HaveOccurred())
			return consumer
		}

		// Consume pulls continuously, each pull request on a fresh inbox.
		consumePayload := []byte("consume probe")
		consumed := make(chan []byte, 1)
		consumeCtx, err := probe("eight", consumePayload).Consume(func(msg jetstream.Msg) {
			if msg.Ack() == nil {
				consumed <- msg.Data()
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer consumeCtx.Stop()
		var got []byte
		Eventually(consumed).WithTimeout(10 * time.Second).Should(Receive(&got))
		Expect(got).To(Equal(consumePayload))

		// Messages is the iterator form of the same pull loop.
		messagesPayload := []byte("messages probe")
		iter, err := probe("seven", messagesPayload).Messages()
		Expect(err).NotTo(HaveOccurred())
		defer iter.Stop()
		msg, err := iter.Next()
		Expect(err).NotTo(HaveOccurred())
		Expect(msg.Data()).To(Equal(messagesPayload))
		Expect(msg.Ack()).To(Succeed())
	}, SpecTimeout(30*time.Second))

	It("serves the tenant's own JetStream publish, consumer and fetch replies on its inbox prefix", func(ctx SpecContext) {
		Expect(testCtx.NatsConn.NewRespInbox()).To(HavePrefix(config.InboxPrefix(testCtx.TenantID) + "."))

		subject := fmt.Sprintf("%s.%s.events.inboxprobe.%s", util.TenantStreamName(testCtx.TenantID), testCtx.TenantID, uuid.NewString())
		payload := []byte("inbox probe")

		// The publish acknowledgement is a reply on the inbox.
		_, err := testCtx.JetStream.Publish(ctx, subject, payload)
		Expect(err).NotTo(HaveOccurred())

		probeConsumer := fmt.Sprintf("%s--nine", testCtx.TenantID)
		probeConsumers = append(probeConsumers, probeConsumer)
		consumer, err := util.CreateJetStreamConsumer(ctx, testCtx.JetStream, testCtx.NatsConn, util.TenantStreamName(testCtx.TenantID), probeConsumer, subject)
		Expect(err).NotTo(HaveOccurred())

		// The fetched batch is delivered to the inbox too.
		batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
		Expect(err).NotTo(HaveOccurred())
		var received [][]byte
		for msg := range batch.Messages() {
			received = append(received, msg.Data())
			Expect(msg.Ack()).To(Succeed())
		}
		Expect(batch.Error()).NotTo(HaveOccurred())
		Expect(received).To(Equal([][]byte{payload}))
	}, SpecTimeout(30*time.Second))
})

// fixed returns a subject builder that ignores the tenant and yields subject.
func fixed(subject string) func(string) string {
	return func(string) string { return subject }
}
