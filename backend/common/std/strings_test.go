package std_test

import (
	"sync"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/std"
)

func TestTitle(t *testing.T) {
	t.Parallel()

	for _, tc := range []caseType{
		{input: "pyck", expected: "Pyck"},
		{input: "main data", expected: "Main Data"},
		{input: "PYCK", expected: "Pyck"},
		{input: "", expected: ""},
	} {
		if got := std.Title(tc.input); got != tc.expected {
			t.Errorf("Title(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// A shared cases.Caser corrupts its buffer when two goroutines transform at
// once, which the race detector reports and which eventually panics with a slice
// bounds error.
func TestTitleIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	const goroutines = 16

	var wait sync.WaitGroup

	wait.Add(goroutines)

	for range goroutines {
		go func() {
			defer wait.Done()

			for range 100 {
				if got := std.Title("main data"); got != "Main Data" {
					t.Errorf("Title = %q, want %q", got, "Main Data")

					return
				}
			}
		}()
	}

	wait.Wait()
}
