//go:build integration

package importexport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	iex "github.com/pyck-ai/pyck/backend/common/importexport"
	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// baseFixture is the single-pass round-trip fixture: it defines all DataType
// records first (upsert by slug), then every other entity in dependency
// order using $refid aliases for cross-service references. DataMixin entities
// reference their DataType by an exact (slug, version) $ref; ids are
// server-generated and never carried in the data.
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
	// Provision a fresh tenant + a machine user with the writer grant plus
	// the per-service gate roles (the round-trip crosses every gated
	// subgraph), mint a PAT and wait for it to be accepted, then build the
	// import/export registry bound to that PAT so the whole round-trip is
	// confined to this tenant.
	reg, ok := s.provisionTenantRegistry("round-trip")
	if !ok {
		return
	}
	s.reg = reg

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
			`{"__typename": "Customer", "dataTypeID": {"$ref": {"__typename": "DataType", "slug": "default-customer", "version": 1}}, "data": {"name": "Test Corp", "code": "TEST-999", "address": "1 Test St"}}`+"\n",
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

	// -------------------------------------------------------------------------
	// Stage 6: Reference edges — Repository FKs export as (slug/name) $refs, and
	// the hierarchy is emitted parents-first (dependency order), so the file
	// re-imports without dangling references.
	// -------------------------------------------------------------------------
	s.Run("repository references are $refs in dependency order", func() {
		// Full export so the reference targets (DataType, Location) are in the
		// set and Repository's FKs can be rewritten. A partial export missing a
		// target omits that FK (a raw id has no portable value).
		r := s.Require()
		dir := s.T().TempDir()
		exp := iex.NewExporter(s.reg, iex.WithExportOutput(&bytes.Buffer{}))
		r.NoError(exp.ExportToDir(s.Ctx, dir, nil), "export all")

		recs := readJSONL(s.T(), dir+"/repository.jsonl")
		pos := map[string]int{}
		for i, rec := range recs {
			name, _ := rec["name"].(string)
			pos[name] = i

			// dataTypeID is always present and must be a $ref, never a raw uuid.
			assertIsRef(s.T(), name, "dataTypeID", rec["dataTypeID"])
			// locationID / parentID are optional, but when present must be $refs.
			if v, ok := rec["locationID"]; ok {
				assertIsRef(s.T(), name, "locationID", v)
			}
			if v, ok := rec["parentID"]; ok {
				assertIsRef(s.T(), name, "parentID", v)
			}
		}

		// The base fixture hierarchy: Main -> Aisle-1 -> Shelf-1 must appear in
		// parents-first order.
		chain := []string{"Warehouse-Alpha-Main", "Alpha-Aisle-1", "Alpha-A1-Shelf-1"}
		for i := 1; i < len(chain); i++ {
			parent, child := chain[i-1], chain[i]
			if _, ok := pos[parent]; !ok {
				continue // base fixture not imported in this run
			}
			if pos[parent] >= pos[child] {
				s.T().Errorf("expected %q (pos %d) before %q (pos %d)", parent, pos[parent], child, pos[child])
			}
		}
	})

	// -------------------------------------------------------------------------
	// Stage 7: Multiple versions of one slug — the base fixture defines
	// default-inventory-item v1/v2/v3 and pins the BOLT-M8-50 family to each.
	// Verify all three versions export and each item keeps its exact pin.
	// -------------------------------------------------------------------------
	s.Run("multiple datatype versions round-trip with per-entity pins", func() {
		r := s.Require()
		dir := s.T().TempDir()
		exp := iex.NewExporter(s.reg, iex.WithExportOutput(&bytes.Buffer{}))
		r.NoError(exp.ExportToDir(s.Ctx, dir, nil), "export all")

		// All three versions of the slug are present.
		versions := map[float64]bool{}
		for _, rec := range readJSONL(s.T(), dir+"/datatype.jsonl") {
			if rec["slug"] == "default-inventory-item" {
				if v, ok := rec["version"].(float64); ok {
					versions[v] = true
				}
			}
		}
		for _, want := range []float64{1, 2, 3} {
			if !versions[want] {
				s.T().Errorf("expected default-inventory-item version %v in export, got %v", want, versions)
			}
		}

		// Each BOLT-M8-50* item keeps its exact (slug, version) pin.
		wantPin := map[string]float64{"BOLT-M8-50": 1, "BOLT-M8-50-V2": 2, "BOLT-M8-50-V3": 3}
		gotPin := map[string]float64{}
		for _, rec := range readJSONL(s.T(), dir+"/inventoryitem.jsonl") {
			if sku, _ := rec["sku"].(string); wantPin[sku] != 0 {
				gotPin[sku] = dataTypeRefVersion(s.T(), sku, rec)
			}
		}
		for sku, want := range wantPin {
			if gotPin[sku] != want {
				s.T().Errorf("item %q pinned to version %v, want %v", sku, gotPin[sku], want)
			}
		}
	})
}

// TestDataTypeVersionRoundTrip proves that multiple versions of one DataType
// slug, and the entities pinned to each exact version, survive an
// export → import round trip. DataType ids are server-generated and never
// exported; the entity → DataType reference travels as a (slug, version) $ref.
func (s *ImportExportSuite) TestDataTypeVersionRoundTrip() {
	// This test runs against its own fresh tenant + registry so it never
	// depends on (or pollutes) the round-trip test's state.
	reg, ok := s.provisionTenantRegistry("versioned")
	if !ok {
		return
	}

	const slug = "versioned-location"
	fixture := strings.Join([]string{
		`{"__typename":"DataType","name":"Versioned Location v1","slug":"` + slug + `","version":1,"entity":"Location","jsonSchema":"{\"type\":\"object\"}"}`,
		`{"__typename":"DataType","name":"Versioned Location v2","slug":"` + slug + `","version":2,"entity":"Location","jsonSchema":"{\"type\":\"object\"}"}`,
		`{"__typename":"Location","name":"Versioned-Loc-V1","dataTypeID":{"$ref":{"__typename":"DataType","slug":"` + slug + `","version":1}},"data":{}}`,
		`{"__typename":"Location","name":"Versioned-Loc-V2","dataTypeID":{"$ref":{"__typename":"DataType","slug":"` + slug + `","version":2}},"data":{}}`,
		"",
	}, "\n")

	fixtureFile := s.T().TempDir() + "/versioned.jsonl"
	s.Require().NoError(os.WriteFile(fixtureFile, []byte(fixture), 0o600))

	// Import the two versions and the two pinned locations. Re-running the test
	// is idempotent: DataTypes skip on (slug, version), Locations upsert by name.
	s.Run("import versioned fixture", func() {
		r := s.Require()
		var output bytes.Buffer
		imp := iex.NewImporter(reg, iex.WithOutput(&output))
		result, err := imp.ImportFiles(s.Ctx, []string{fixtureFile})
		r.NoError(err, "import versioned fixture\noutput:\n%s", output.String())
		s.T().Logf("import: %s", formatResult(result))
		r.Empty(result.Errors, "unexpected import errors")
	})

	// Export and assert both versions are present (no id) and each location's
	// $ref carries the exact version it was pinned to.
	s.Run("export preserves versions and pins", func() {
		r := s.Require()
		dir := s.T().TempDir()
		exp := iex.NewExporter(reg, iex.WithExportOutput(&bytes.Buffer{}))
		r.NoError(exp.ExportToDir(s.Ctx, dir, []string{"DataType", "Location"}), "export")

		// DataType rows for our slug: versions {1,2}, no server-generated id.
		versions := map[float64]bool{}
		for _, rec := range readJSONL(s.T(), dir+"/datatype.jsonl") {
			if rec["slug"] != slug {
				continue
			}
			if _, hasID := rec["id"]; hasID {
				s.T().Errorf("exported DataType %q still carries a server-generated id: %v", slug, rec["id"])
			}
			v, ok := rec["version"].(float64)
			if !ok {
				s.T().Errorf("exported DataType %q missing version: %v", slug, rec["version"])
				continue
			}
			versions[v] = true
		}
		if !versions[1] || !versions[2] {
			s.T().Errorf("expected both versions 1 and 2 of %q, got %v", slug, versions)
		}

		// Locations carry a (slug, version) $ref — never a raw uuid — pointing at
		// the exact version each was pinned to.
		wantPin := map[string]float64{"Versioned-Loc-V1": 1, "Versioned-Loc-V2": 2}
		gotPin := map[string]float64{}
		for _, rec := range readJSONL(s.T(), dir+"/location.jsonl") {
			name, _ := rec["name"].(string)
			if _, want := wantPin[name]; !want {
				continue
			}
			gotPin[name] = dataTypeRefVersion(s.T(), name, rec)
		}
		for name, want := range wantPin {
			if gotPin[name] != want {
				s.T().Errorf("location %q pinned to version %v, want %v", name, gotPin[name], want)
			}
		}
	})
}

// diverseTypes are the entity types the diverse fixture covers, in
// dependency order (reference targets before referrers) so the exported
// files can be re-imported in this exact order.
var diverseTypes = []string{"DataType", "Location", "Device", "Repository", "InventoryItem"}

// TestDiverseVersionedReimport proves the full portability contract on a
// diverse dataset: four DataType slug families with one to four versions
// each, entities pinned across all of those versions, a Repository
// hierarchy (parentID self-refs), and Location references. The export of a
// tenant must import cleanly into a *fresh* tenant, and that tenant's own
// export must contain the same records — ids aside, nothing may be lost,
// duplicated, or re-pinned along the way.
func (s *ImportExportSuite) TestDiverseVersionedReimport() {
	exportDirA := s.T().TempDir()
	exportDirB := s.T().TempDir()

	// Tenant A: import the hand-written diverse fixture and export it.
	regA, ok := s.provisionTenantRegistry("A")
	if !ok {
		return
	}
	if !s.Run("import diverse fixture into tenant A", func() {
		r := s.Require()
		var output bytes.Buffer
		imp := iex.NewImporter(regA, iex.WithOutput(&output))
		result, err := imp.ImportFiles(s.Ctx, []string{"testdata/diverse.jsonl"})
		r.NoError(err, "import diverse fixture\noutput:\n%s", output.String())
		s.T().Logf("tenant A import: %s", formatResult(result))
		r.Empty(result.Errors, "unexpected import errors")
	}) {
		return
	}
	if !s.Run("export tenant A", func() {
		exp := iex.NewExporter(regA, iex.WithExportOutput(&bytes.Buffer{}))
		s.Require().NoError(exp.ExportToDir(s.Ctx, exportDirA, diverseTypes), "export tenant A")
	}) {
		return
	}

	// Tenant B: import tenant A's export, then export again.
	regB, ok := s.provisionTenantRegistry("B")
	if !ok {
		return
	}
	// Import the export DIRECTORY, not a hand-ordered file list: an operator
	// re-importing an environment passes the directory, and the importer must
	// order the per-type files itself. Alphabetically "customer.jsonl" and
	// "devicelocation.jsonl" precede the types they reference, so a naive
	// expansion cannot resolve their $refs.
	if !s.Run("re-import tenant A's export directory into tenant B", func() {
		r := s.Require()
		var output bytes.Buffer
		imp := iex.NewImporter(regB, iex.WithOutput(&output))
		result, err := imp.ImportFiles(s.Ctx, []string{exportDirA})
		r.NoError(err, "re-import export\noutput:\n%s", output.String())
		s.T().Logf("tenant B import: %s", formatResult(result))
		r.Empty(result.Errors, "unexpected re-import errors")
	}) {
		return
	}
	if !s.Run("export tenant B", func() {
		exp := iex.NewExporter(regB, iex.WithExportOutput(&bytes.Buffer{}))
		s.Require().NoError(exp.ExportToDir(s.Ctx, exportDirB, diverseTypes), "export tenant B")
	}) {
		return
	}

	// The exported datatype file must itself be re-importable: the server's
	// append-only reconcile rejects any version at or below a family's
	// current MAX, so within datatype.jsonl each slug family's versions must
	// strictly ascend in file order. canonicalRecords sorts before
	// comparing, so the equivalence check below is structurally blind to
	// ordering — this assertion is what pins it.
	s.Run("datatype exports are import-ordered", func() {
		for _, dir := range []string{exportDirA, exportDirB} {
			assertVersionsAscendPerSlug(s.T(), dir+"/datatype.jsonl")
		}
	})

	// The two exports must carry the same records. Server-generated ids are
	// the only legitimate difference between the tenants.
	s.Run("exports are equivalent", func() {
		for _, t := range diverseTypes {
			name := strings.ToLower(t) + ".jsonl"
			a := canonicalRecords(s.T(), exportDirA+"/"+name)
			b := canonicalRecords(s.T(), exportDirB+"/"+name)
			s.Require().Equal(a, b, "%s differs between tenant A and tenant B exports", name)
			s.T().Logf("%s: %d records identical", name, len(a))
		}
	})
}

// provisionTenantRegistry registers a fresh tenant with a writer PAT and
// returns an import/export registry bound to it. The bool mirrors s.Run's
// success so callers can bail out of the remaining stages.
func (s *ImportExportSuite) provisionTenantRegistry(label string) (*iex.Registry, bool) {
	reg, _, ok := s.provisionTenant(label)
	return reg, ok
}

// provisionTenant additionally returns a management API client bound to the
// same tenant, for the mutations the import/export registry does not expose.
func (s *ImportExportSuite) provisionTenant(label string) (*iex.Registry, managementapi.Client, bool) {
	tenant := fixtures.NewTenant()
	var (
		reg    *iex.Registry
		client managementapi.Client
	)
	ok := s.Run("register tenant "+label+" and provision writer PAT", func() {
		r := s.Require()
		rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, tenant)
		r.NoError(err)
		s.DeferTenantCleanup(rt.ID)

		p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
		r.NoError(err, "provision writer PAT")
		s.T().Logf("tenant %s: tenantID=%s userID=%s PAT ready", label, rt.ID, p.UserID)

		reg, err = gateway.NewImportExportRegistry(s.Cfg, p.PAT)
		r.NoError(err)
		client = gateway.NewClientForTenant(s.Cfg, p.PAT, rt.ID)
	})
	return reg, client, ok
}

// A row pinned to a version that was later deleted must not export silently:
// the deleted version is hidden from the DataType listing, so emitting the
// row without its reference would either fail a re-import with an error
// naming neither the row nor the version, or import the row unpinned.
func (s *ImportExportSuite) TestExportRefusesPinToDeletedVersion() {
	reg, client, ok := s.provisionTenant("deleted-pin")
	if !ok {
		return
	}

	const slug = "deleted-pin-location"
	fixture := strings.Join([]string{
		`{"__typename":"DataType","name":"Deleted Pin v1","slug":"` + slug + `","version":1,"entity":"Location","jsonSchema":"{\"type\":\"object\"}"}`,
		`{"__typename":"DataType","name":"Deleted Pin v2","slug":"` + slug + `","version":2,"entity":"Location","jsonSchema":"{\"type\":\"object\"}"}`,
		`{"__typename":"Location","name":"Pinned-To-Deleted","dataTypeID":{"$ref":{"__typename":"DataType","slug":"` + slug + `","version":1}},"data":{}}`,
		"",
	}, "\n")

	fixtureFile := s.T().TempDir() + "/deleted-pin.jsonl"
	s.Require().NoError(os.WriteFile(fixtureFile, []byte(fixture), 0o600))

	if !s.Run("import two versions and pin a location to v1", func() {
		r := s.Require()
		var output bytes.Buffer
		imp := iex.NewImporter(reg, iex.WithOutput(&output))
		result, err := imp.ImportFiles(s.Ctx, []string{fixtureFile})
		r.NoError(err, "import fixture\noutput:\n%s", output.String())
		r.Empty(result.Errors, "unexpected import errors")
	}) {
		return
	}

	if !s.Run("delete the pinned version", func() {
		r := s.Require()
		one := 1
		orderField := managementapi.DataTypeOrderFieldVersion
		res, err := client.GetDataTypes(s.Ctx, managementapi.GetDataTypesArgs{
			First:   &one,
			OrderBy: &managementapi.DataTypeOrder{Direction: managementapi.OrderDirectionAsc, Field: &orderField},
			Where:   &managementapi.DataTypeWhereInput{Slug: ptr(slug)},
		})
		r.NoError(err)
		edges := res.GetDataTypes().GetEdges()
		r.Len(edges, 1, "expected v1 of %q", slug)
		_, err = client.DeleteDataType(s.Ctx, managementapi.DeleteDataTypeArgs{Id: edges[0].GetNode().ID})
		r.NoError(err, "delete v1")
	}) {
		return
	}

	s.Run("export fails naming the row and the missing target", func() {
		exp := iex.NewExporter(reg, iex.WithExportOutput(&bytes.Buffer{}))
		err := exp.ExportToDir(s.Ctx, s.T().TempDir(), []string{"DataType", "Location"})
		s.Require().Error(err, "export must refuse a pin it cannot resolve")
		s.Require().ErrorIs(err, iex.ErrUnresolvedReference)
		s.Assert().Contains(err.Error(), "Pinned-To-Deleted")
		s.Assert().Contains(err.Error(), "dataTypeID")
	})
}

func ptr[T any](v T) *T { return &v }

// canonicalRecords parses a JSONL export and returns one canonical JSON
// string per record, sorted. Server-generated ids are stripped — they are
// the only field allowed to differ between two tenants holding the same
// data; everything else (including (slug, version) $refs) must match.
func canonicalRecords(t *testing.T, path string) []string {
	t.Helper()
	recs := readJSONL(t, path)
	out := make([]string, 0, len(recs))
	for _, rec := range recs {
		delete(rec, "id")
		b, err := json.Marshal(rec) // map keys marshal sorted
		if err != nil {
			t.Fatalf("canonicalize %s: %v", path, err)
		}
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

// assertVersionsAscendPerSlug fails unless, within the given JSONL file,
// records sharing a slug appear with strictly ascending version numbers —
// the order the append-only DataType import contract requires.
func assertVersionsAscendPerSlug(t *testing.T, path string) {
	t.Helper()
	last := map[string]float64{}
	for i, rec := range readJSONL(t, path) {
		slug, _ := rec["slug"].(string)
		version, ok := rec["version"].(float64)
		if slug == "" || !ok {
			t.Errorf("%s record %d: missing slug/version: %v", path, i, rec)
			continue
		}
		if prev, seen := last[slug]; seen && version <= prev {
			t.Errorf("%s: slug %q version %v emitted after version %v — file cannot be re-imported",
				path, slug, version, prev)
		}
		last[slug] = version
	}
}

// assertIsRef fails unless v is a {"$ref": ...} object (not a raw uuid string).
func assertIsRef(t *testing.T, entity, field string, v any) {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Errorf("%s.%s is not a $ref (raw value: %v)", entity, field, v)
		return
	}
	if _, ok := m["$ref"]; !ok {
		t.Errorf("%s.%s has no $ref: %v", entity, field, m)
	}
}

// readJSONL parses a JSONL export file into one map per non-empty line.
func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var records []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse %s line %q: %v", path, line, err)
		}
		records = append(records, rec)
	}
	return records
}

// dataTypeRefVersion extracts the version from a record's
// dataTypeID: {"$ref": {"slug":..., "version":...}}, failing the test if the
// reference is a raw uuid instead of a (slug, version) $ref.
func dataTypeRefVersion(t *testing.T, name string, rec map[string]any) float64 {
	t.Helper()
	raw, ok := rec["dataTypeID"].(map[string]any)
	if !ok {
		t.Fatalf("%q: dataTypeID is not a $ref (raw value: %v)", name, rec["dataTypeID"])
	}
	ref, ok := raw["$ref"].(map[string]any)
	if !ok {
		t.Fatalf("%q: dataTypeID has no $ref object: %v", name, raw)
	}
	version, ok := ref["version"].(float64)
	if !ok {
		t.Fatalf("%q: $ref missing version: %v", name, ref)
	}
	return version
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
