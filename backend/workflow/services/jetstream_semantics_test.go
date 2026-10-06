package services_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the JetStream behaviour the router's pause design rests on.
// They exercise the server, not the router.

func semanticsConsumer(t *testing.T, e *natsEnv, maxDeliver int) jetstream.Consumer {
	t.Helper()

	c, err := e.js.CreateOrUpdateConsumer(context.Background(), consumerStream, jetstream.ConsumerConfig{
		Durable:        "semantics",
		FilterSubjects: []string{consumerStream + ".*.crud.*.*.*.*"},
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        time.Second,
		MaxDeliver:     maxDeliver,
	})
	require.NoError(t, err)

	return c
}

func fetchOne(t *testing.T, c jetstream.Consumer) (jetstream.Msg, uint64) {
	t.Helper()

	batch, err := c.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)

	for msg := range batch.Messages() {
		meta, err := msg.Metadata()
		require.NoError(t, err)

		return msg, meta.NumDelivered
	}

	return nil, 0
}

// A NAK, with or without delay, counts as a delivery: the redelivery carries
// NumDelivered+1. So NAKing through an outage burns one attempt per NAK.
func TestJetStream_NakIncrementsNumDelivered(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)
	c := semanticsConsumer(t, e, 10)
	e.publish(mutationSubject())

	msg, n := fetchOne(t, c)
	require.NotNil(t, msg)
	assert.EqualValues(t, 1, n)
	require.NoError(t, msg.NakWithDelay(50*time.Millisecond))

	msg, n = fetchOne(t, c)
	require.NotNil(t, msg)
	assert.EqualValues(t, 2, n, "the redelivery after a NAK is delivery 2")
	require.NoError(t, msg.Ack())
}

// Once an event has been delivered MaxDeliver times, the server does not
// deliver it again, whether it was NAKed or left to time out. A NAK on the last
// delivery therefore loses the event.
func TestJetStream_NoRedeliveryAfterMaxDeliver(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)
	c := semanticsConsumer(t, e, 2)
	e.publish(mutationSubject())

	for want := uint64(1); want <= 2; want++ {
		msg, n := fetchOne(t, c)
		require.NotNil(t, msg)
		require.Equal(t, want, n)
		require.NoError(t, msg.NakWithDelay(20*time.Millisecond))
	}

	msg, _ := fetchOne(t, c)
	assert.Nil(t, msg, "no third delivery with MaxDeliver=2")
}

// InProgress resets AckWait, so an event can be held far longer than AckWait
// without being redelivered or counted as a new delivery.
func TestJetStream_InProgressHoldsPastAckWait(t *testing.T) {
	t.Parallel()

	e := newNatsEnv(t)
	c := semanticsConsumer(t, e, 10) // AckWait 1s
	e.publish(mutationSubject())

	msg, n := fetchOne(t, c)
	require.NotNil(t, msg)
	require.EqualValues(t, 1, n)

	for range 6 {
		time.Sleep(400 * time.Millisecond)
		require.NoError(t, msg.InProgress())
	}

	info, err := c.Info(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, info.Delivered.Stream, "held 2.4s with a 1s AckWait and never redelivered")
	assert.Zero(t, info.NumRedelivered)
	require.NoError(t, msg.Ack())
}
