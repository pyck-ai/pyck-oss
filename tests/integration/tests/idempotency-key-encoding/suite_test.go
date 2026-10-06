//go:build integration

// Package idempotencykeyencoding_test checks, against a live stack and for
// every service, that an Idempotency-Key which is not valid UTF-8 is refused
// as a client error. Go's HTTP server passes bytes 0x80-0xff through; the key
// used to reach the idempotency store, where Postgres refused it, and the
// caller got a 500 with an ERROR log line.
//
// Run with:
//
//	source ./scripts/envrc.sh && go test -tags=integration -v -count=1 ./tests/idempotency-key-encoding/...
package idempotencykeyencoding_test

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestIdempotencyKeyEncoding runs the suite.
func TestIdempotencyKeyEncoding(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(KeyEncodingSuite))
}
