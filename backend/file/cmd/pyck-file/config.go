package main

import "time"

const (
	// idempotencyJanitorInterval is how often the background goroutine
	// wakes up to prune committed idempotency rows.
	idempotencyJanitorInterval = 5 * time.Minute

	// idempotencyJanitorTTL is the lifetime of a committed idempotency
	// record. Stripe-style: the client may safely retry the same key
	// for this long after a successful response and get the cached
	// body back.
	idempotencyJanitorTTL = 24 * time.Hour
)
