package db_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/db"
)

// TestBuildPoolUri_AppliesIsolation verifies that the isolation level passed
// through buildPoolUri (the unit underlying WithWriterIsolation /
// WithReaderIsolation) flows into the resulting URL's query string.
func TestBuildPoolUri_AppliesIsolation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		isolation string
	}{
		{"serializable", "serializable"},
		{"read committed", "read committed"},
		{"repeatable read", "repeatable read"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			raw, err := db.BuildPoolUri(
				"postgres://user:pass@host:5432/dbname",
				"inventory",
				tc.isolation,
			)
			if err != nil {
				t.Fatalf("buildPoolUri returned error: %v", err)
			}

			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("result URL is unparseable: %v", err)
			}

			gotIso := parsed.Query().Get("default_transaction_isolation")
			if gotIso != tc.isolation {
				t.Errorf(
					"default_transaction_isolation = %q, want %q (raw url: %q)",
					gotIso, tc.isolation, raw,
				)
			}

			if got := parsed.Query().Get("search_path"); got != "inventory" {
				t.Errorf("search_path = %q, want %q", got, "inventory")
			}
		})
	}
}

// TestOptions_FlowThrough verifies that WithWriterIsolation /
// WithReaderIsolation mutate the option struct as expected. Combined with
// TestBuildPoolUri_AppliesIsolation above, this proves the full path:
// caller-supplied option -> driverOpts field -> URL query arg.
func TestOptions_FlowThrough(t *testing.T) {
	t.Parallel()

	opts := db.NewDriverOpts()

	if opts.WriterIsolation() != "serializable" {
		t.Errorf("default writerIsolation = %q, want %q",
			opts.WriterIsolation(), "serializable")
	}
	if opts.ReaderIsolation() != "read committed" {
		t.Errorf("default readerIsolation = %q, want %q",
			opts.ReaderIsolation(), "read committed")
	}

	opts.ApplyOption(db.WithWriterIsolation("read committed"))
	opts.ApplyOption(db.WithReaderIsolation("repeatable read"))

	if got := opts.WriterIsolation(); got != "read committed" {
		t.Errorf("after WithWriterIsolation: writerIsolation = %q, want %q",
			got, "read committed")
	}
	if got := opts.ReaderIsolation(); got != "repeatable read" {
		t.Errorf("after WithReaderIsolation: readerIsolation = %q, want %q",
			got, "repeatable read")
	}
}

// TestBuildPoolUri_PreservesExistingQuery confirms that user-supplied query
// params on the input URL are preserved (not clobbered) by applyQueryArgs.
// This guards against regressions if ops add e.g. sslmode=require to the URL.
func TestBuildPoolUri_PreservesExistingQuery(t *testing.T) {
	t.Parallel()

	raw, err := db.BuildPoolUri(
		"postgres://user:pass@host:5432/db?sslmode=require&application_name=pyck",
		"inventory",
		"read committed",
	)
	if err != nil {
		t.Fatalf("buildPoolUri returned error: %v", err)
	}

	if !strings.Contains(raw, "sslmode=require") {
		t.Errorf("expected sslmode=require to survive, got %q", raw)
	}
	if !strings.Contains(raw, "application_name=pyck") {
		t.Errorf("expected application_name=pyck to survive, got %q", raw)
	}
}
