//go:build integration

package importexport

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	iex "github.com/pyck-ai/pyck/backend/common/importexport"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// baseFixture is the single-pass round-trip fixture: it defines all DataType
// records first (upsert by slug), then every other entity in dependency
// order using $refid aliases for cross-service references.
const baseFixture = "testdata/base.jsonl"

// ImportExportSuite drives a full import/export round-trip through the
// federated gateway as a fresh, tenant-scoped writer — see the package doc.
type ImportExportSuite struct {
	tests.Base

	// reg holds all five subgraphs' entities, bound to the tenant PAT.
	reg *iex.Registry
}

//nolint:gocyclo // sequential stages, each depends on the previous registry/tenant.
func (s *ImportExportSuite) TestImportExportRoundTrip() {
	tenant := fixtures.NewTenant()
	s.T().Logf("tenant=%s", tenant.Name)

	// Provision a fresh tenant + a machine user with the writer grant plus
	// the per-service gate roles (the round-trip crosses every gated
	// subgraph), mint a PAT and wait for it to be accepted, then build the
	// import/export registry bound to that PAT so the whole round-trip is
	// confined to this tenant.
	if !s.Run("register tenant and provision writer PAT", func() {
		r := s.Require()
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
		r.NoError(err)
		s.DeferTenantCleanup(rt.ID)

		p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
		r.NoError(err, "provision writer PAT")
		s.T().Logf("tenantID=%s userID=%s PAT ready", rt.ID, p.UserID)

		reg, err := gateway.NewImportExportRegistry(s.Cfg, p.PAT)
		r.NoError(err)
		s.reg = reg
	}) {
		return
	}

	// -------------------------------------------------------------------------
	// Stage 1: single-pass import of all entity types ($refid aliases resolve
	// Customer/Supplier → Order references in one pass). ItemMovement is
	// expected to fail (insufficient stock), so up to one error is tolerated.
	// -------------------------------------------------------------------------
	s.Run("import all entities", func() {
		var output bytes.Buffer
		imp := iex.NewImporter(s.reg,
			iex.WithOutput(&output),
			iex.WithContinueOnError(true),
		)

		// With ContinueOnError, ImportFiles always returns a nil error — any
		// failure (including a missing/unreadable fixture) lands in
		// result.Errors instead, so we inspect that, not the return value.
		result, _ := imp.ImportFiles(s.Ctx, []string{baseFixture})
		s.T().Logf("stage 1: %s", formatResult(result))

		if result.Created+result.Updated == 0 {
			s.T().Logf("import output:\n%s", output.String())
			s.T().Error("expected at least some entities to be created or updated")
		}
		if len(result.Errors) > 1 {
			s.T().Logf("import output:\n%s", output.String())
			for _, e := range result.Errors {
				s.T().Errorf("unexpected error at %s:%d: %v", e.Record.Source, e.Record.Line, e.Err)
			}
		}
	})

	// -------------------------------------------------------------------------
	// Stage 2: re-import — upsert entities (keyed by slug/identity) update.
	// -------------------------------------------------------------------------
	s.Run("re-import idempotency", func() {
		var output bytes.Buffer
		imp := iex.NewImporter(s.reg,
			iex.WithOutput(&output),
			iex.WithContinueOnError(true),
		)

		result, _ := imp.ImportFiles(s.Ctx, []string{baseFixture})
		s.T().Logf("stage 2: %s", formatResult(result))

		if result.Updated == 0 {
			s.T().Error("expected upsert entities to be updated on re-import")
		}
	})

	// -------------------------------------------------------------------------
	// Stage 3: export all types and verify non-empty counts.
	// -------------------------------------------------------------------------
	s.Run("export all types", func() {
		r := s.Require()
		dir := s.T().TempDir()
		exp := iex.NewExporter(s.reg, iex.WithExportOutput(&bytes.Buffer{}))

		r.NoError(exp.ExportToDir(s.Ctx, dir, nil), "export failed")

		entries, _ := os.ReadDir(dir)
		r.NotEmpty(entries, "no export files created")
		s.T().Logf("stage 3: exported %d entity types", len(entries))

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
				s.T().Errorf("missing export: %s", name)
				continue
			}
			lines := countExportedLines(data)
			if lines == 0 {
				s.T().Errorf("export file %s is empty", name)
			}
			s.T().Logf("  %s = %d entities", name, lines)
		}
	})

	// -------------------------------------------------------------------------
	// Stage 4: create-only entities — export includes id, re-import skips.
	// -------------------------------------------------------------------------
	s.Run("create-only skip on reimport", func() {
		r := s.Require()
		dir := s.T().TempDir()
		exp := iex.NewExporter(s.reg, iex.WithExportOutput(&bytes.Buffer{}))
		r.NoError(exp.ExportToDir(s.Ctx, dir, []string{"Customer", "Supplier", "DeviceLocation"}), "export create-only")

		for _, name := range []string{"customer.jsonl", "supplier.jsonl", "devicelocation.jsonl"} {
			data, err := os.ReadFile(dir + "/" + name)
			if err != nil {
				s.T().Errorf("missing: %s", name)
				continue
			}
			if !bytes.Contains(data, []byte(`"id"`)) {
				s.T().Errorf("%s: exported create-only entity missing 'id' field", name)
			}
		}

		var output bytes.Buffer
		imp := iex.NewImporter(s.reg, iex.WithOutput(&output))
		result, err := imp.ImportFiles(s.Ctx, []string{
			dir + "/customer.jsonl",
			dir + "/supplier.jsonl",
			dir + "/devicelocation.jsonl",
		})
		r.NoError(err, "reimport create-only\noutput:\n%s", output.String())
		s.T().Logf("stage 4: %s", formatResult(result))

		if result.Skipped == 0 {
			s.T().Error("expected create-only entities to be skipped on reimport")
		}
		if result.Created != 0 {
			s.T().Errorf("expected 0 created on reimport, got %d", result.Created)
		}
	})

	// -------------------------------------------------------------------------
	// Stage 5: create-only without id — always created (no skip).
	// -------------------------------------------------------------------------
	s.Run("create-only without id creates new", func() {
		r := s.Require()
		tmpFile := s.T().TempDir() + "/new-customer.jsonl"
		r.NoError(os.WriteFile(tmpFile, []byte(
			`{"__typename": "Customer", "dataTypeSlug": "default-customer", "data": {"name": "Test Corp", "code": "TEST-999", "address": "1 Test St"}}`+"\n",
		), 0o600))

		var output bytes.Buffer
		imp := iex.NewImporter(s.reg, iex.WithOutput(&output))
		result, err := imp.ImportFiles(s.Ctx, []string{tmpFile})
		r.NoError(err, "import new create-only\noutput:\n%s", output.String())
		s.T().Logf("stage 5: %s", formatResult(result))

		if result.Created != 1 {
			s.T().Errorf("expected 1 created, got %d", result.Created)
		}

		// No id, so a second import creates a duplicate (by design).
		result2, err := imp.ImportFiles(s.Ctx, []string{tmpFile})
		r.NoError(err, "duplicate import")
		if result2.Created != 1 {
			s.T().Errorf("expected duplicate to be created (no id), got created=%d", result2.Created)
		}
	})
}

// formatResult returns a human-readable summary of an import result.
func formatResult(r *iex.ImportResult) string {
	return fmt.Sprintf("created=%d updated=%d skipped=%d errors=%d",
		r.Created, r.Updated, r.Skipped, len(r.Errors))
}

// countExportedLines counts non-empty lines in a JSONL byte slice.
func countExportedLines(data []byte) int {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) > 0 {
			count++
		}
	}
	return count
}
