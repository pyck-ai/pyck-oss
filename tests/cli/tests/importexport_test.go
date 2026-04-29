package tests_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/pyck-ai/pyck/backend/common/importexport"
)

// TestImportExportRoundTrip tests the full import/export flow against running services.
// Requires: task init up (all services running on localhost).
//
// The base.jsonl fixture uses $refid aliases for single-pass import of all 17
// entity types including orders and standalone items — no templates or multi-stage
// workarounds needed.
//
//nolint:paralleltest // subtests are sequential — each stage depends on the previous
func TestImportExportRoundTrip(t *testing.T) {
	gatewayURL := requireGateway(t)
	token := loadAuthToken(t)
	ctx := context.Background()

	reg := buildTestRegistry(t, gatewayURL, token)

	// -------------------------------------------------------------------------
	// Stage 1: Single-pass import of all entity types
	// Uses $refid aliases for Customer/Supplier → Order references
	// -------------------------------------------------------------------------
	t.Run("import all entities", func(t *testing.T) {
		var output bytes.Buffer
		imp := importexport.NewImporter(reg,
			importexport.WithOutput(&output),
			importexport.WithContinueOnError(true),
		)

		result, _ := imp.ImportFiles(ctx, []string{"../testdata/base.jsonl"})

		t.Logf("stage 1: %s", formatResult(result))

		// Upsert entities are created on first run, updated on re-run.
		// Create-only entities are always created (no identity field, no id in fixture).
		// ItemMovement is expected to fail (insufficient stock) — 1 error is OK.
		total := result.Created + result.Updated
		if total == 0 {
			t.Error("expected at least some entities to be created or updated")
		}
		if len(result.Errors) > 1 {
			t.Logf("output:\n%s", output.String())
			for _, e := range result.Errors {
				t.Errorf("unexpected error at %s:%d: %v", e.Record.Source, e.Record.Line, e.Err)
			}
		}
	})

	// -------------------------------------------------------------------------
	// Stage 2: Re-import — upsert entities updated, create-only entities duplicated
	// -------------------------------------------------------------------------
	t.Run("re-import idempotency", func(t *testing.T) {
		var output bytes.Buffer
		imp := importexport.NewImporter(reg,
			importexport.WithOutput(&output),
			importexport.WithContinueOnError(true),
		)

		result, _ := imp.ImportFiles(ctx, []string{"../testdata/base.jsonl"})

		t.Logf("stage 2: %s", formatResult(result))

		if result.Updated == 0 {
			t.Error("expected upsert entities to be updated on re-import")
		}
	})

	// -------------------------------------------------------------------------
	// Stage 3: Export all types and verify counts
	// -------------------------------------------------------------------------
	t.Run("export all types", func(t *testing.T) {
		dir := t.TempDir()
		exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))

		if err := exp.ExportToDir(ctx, dir, nil); err != nil {
			t.Fatalf("export failed: %v", err)
		}

		entries, _ := os.ReadDir(dir)
		if len(entries) == 0 {
			t.Fatal("no export files created")
		}

		t.Logf("stage 3: exported %d entity types", len(entries))

		// Verify non-empty exports for all imported entity types.
		for _, name := range []string{
			"datatype.jsonl", "location.jsonl", "device.jsonl",
			"repository.jsonl", "inventoryitem.jsonl", "inventoryitemset.jsonl",
			"customer.jsonl", "supplier.jsonl", "devicelocation.jsonl",
			"pickingorder.jsonl", "pickingorderitem.jsonl",
			"replenishmentorder.jsonl", "replenishmentorderitem.jsonl",
			"receivinginbound.jsonl", "receivinginbounditem.jsonl",
			"repositorymovement.jsonl",
		} {
			data, err := os.ReadFile(dir + "/" + name)
			if err != nil {
				t.Errorf("missing export: %s", name)
				continue
			}
			lines := countExportedLines(data)
			if lines == 0 {
				t.Errorf("export file %s is empty", name)
			}
			t.Logf("  %s = %d entities", name, lines)
		}
	})

	// -------------------------------------------------------------------------
	// Stage 4: Create-only entities — export includes id, re-import skips
	// -------------------------------------------------------------------------
	t.Run("create-only skip on reimport", func(t *testing.T) {
		dir := t.TempDir()
		exp := importexport.NewExporter(reg, importexport.WithExportOutput(&bytes.Buffer{}))
		err := exp.ExportToDir(ctx, dir, []string{"Customer", "Supplier", "DeviceLocation"})
		if err != nil {
			t.Fatalf("export create-only: %v", err)
		}

		// Verify exported files contain "id" field.
		for _, name := range []string{"customer.jsonl", "supplier.jsonl", "devicelocation.jsonl"} {
			data, err := os.ReadFile(dir + "/" + name)
			if err != nil {
				t.Errorf("missing: %s", name)
				continue
			}
			if !bytes.Contains(data, []byte(`"id"`)) {
				t.Errorf("%s: exported create-only entity missing 'id' field", name)
			}
		}

		// Re-import the exported files — should skip all (already exist by id).
		var output bytes.Buffer
		imp := importexport.NewImporter(reg, importexport.WithOutput(&output))
		result, err := imp.ImportFiles(ctx, []string{
			dir + "/customer.jsonl",
			dir + "/supplier.jsonl",
			dir + "/devicelocation.jsonl",
		})
		if err != nil {
			t.Fatalf("reimport create-only: %v\noutput:\n%s", err, output.String())
		}

		t.Logf("stage 4: %s", formatResult(result))

		if result.Skipped == 0 {
			t.Error("expected create-only entities to be skipped on reimport")
		}
		if result.Created != 0 {
			t.Errorf("expected 0 created on reimport, got %d", result.Created)
		}
	})

	// -------------------------------------------------------------------------
	// Stage 5: Create-only without id — always created (no skip)
	// -------------------------------------------------------------------------
	t.Run("create-only without id creates new", func(t *testing.T) {
		tmpFile := t.TempDir() + "/new-customer.jsonl"
		err := os.WriteFile(tmpFile, []byte(
			`{"__typename": "Customer", "dataTypeSlug": "default-customer", "data": {"name": "Test Corp", "code": "TEST-999", "address": "1 Test St"}}`+"\n",
		), 0o600)
		if err != nil {
			t.Fatal(err)
		}

		var output bytes.Buffer
		imp := importexport.NewImporter(reg, importexport.WithOutput(&output))
		result, err := imp.ImportFiles(ctx, []string{tmpFile})
		if err != nil {
			t.Fatalf("import new create-only: %v\noutput:\n%s", err, output.String())
		}

		t.Logf("stage 5: %s", formatResult(result))

		if result.Created != 1 {
			t.Errorf("expected 1 created, got %d", result.Created)
		}

		// Import same file again — no id, so it creates a duplicate (by design).
		result2, err := imp.ImportFiles(ctx, []string{tmpFile})
		if err != nil {
			t.Fatalf("duplicate import: %v", err)
		}
		if result2.Created != 1 {
			t.Errorf("expected duplicate to be created (no id), got created=%d", result2.Created)
		}
	})
}
