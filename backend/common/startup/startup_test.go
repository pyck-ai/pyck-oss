package startup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/startup"
)

func TestCheck_LiveContext(t *testing.T) {
	t.Parallel()

	if err := startup.Check(context.Background(), "some step"); err != nil {
		t.Fatalf("expected nil error for a live context, got %v", err)
	}
}

func TestCheck_CancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := startup.Check(ctx, "migrations")
	if err == nil {
		t.Fatal("expected an error for a cancelled context, got nil")
	}
	if !errors.Is(err, startup.ErrAborted) {
		t.Fatalf("expected error to satisfy errors.Is(err, ErrAborted), got %v", err)
	}
	if got, want := err.Error(), "startup aborted by stop signal: after migrations"; got != want {
		t.Fatalf("error message = %q, want %q", got, want)
	}
}
