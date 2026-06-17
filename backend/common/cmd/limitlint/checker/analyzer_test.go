package checker_test

import (
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/pyck-ai/pyck/backend/common/cmd/limitlint/checker"
)

func TestAnalyzer(t *testing.T) {
	t.Parallel()

	testdata := analysistest.TestData()

	// Discover from the testdata schemas so the wiring matches what main()
	// does in production. This proves the discovery step picks up
	// LimitMixin entities and skips the rest.
	root, err := filepath.Abs(filepath.Join(testdata, "src", "svc"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	entities, err := checker.DiscoverLimitMixinEntities(root)
	if err != nil {
		t.Fatalf("DiscoverLimitMixinEntities: %v", err)
	}
	if !entities["Item"] {
		t.Fatalf("expected Item to be detected as LimitMixin entity; got %v", entities)
	}
	if entities["Event"] {
		t.Fatalf("Event must not be flagged as LimitMixin; got %v", entities)
	}

	a := checker.New(checker.Config{
		IsLimitMixin:        func(_, entity string) bool { return entities[entity] },
		EntGenPackageSuffix: "/ent/gentest",
	})

	analysistest.Run(t, testdata, a, "svc/caller", "svc/ent/gentest")
}

func TestDiscoverLimitMixinEntities(t *testing.T) {
	t.Parallel()

	testdata := analysistest.TestData()
	root := filepath.Join(testdata, "src", "svc")

	got, err := checker.DiscoverLimitMixinEntities(root)
	if err != nil {
		t.Fatalf("DiscoverLimitMixinEntities: %v", err)
	}

	wantHas := []string{"Item"}
	wantMissing := []string{"Event"}

	for _, name := range wantHas {
		if !got[name] {
			t.Errorf("expected %q to be detected, got %v", name, got)
		}
	}
	for _, name := range wantMissing {
		if got[name] {
			t.Errorf("did not expect %q to be detected, got %v", name, got)
		}
	}
}
