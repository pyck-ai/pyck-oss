//go:build integration

package datatypeversioning

// TestAppendOnlyVersions: reusing a slug publishes a new numbered version
// row; dataTypeBySlug always resolves to the latest live one; all versions
// stay listable.
func (s *VersioningSuite) TestAppendOnlyVersions() {
	r := s.Require()
	tc := s.newTenantClient()
	slug := dvSlug("append")

	v1 := tc.mustCreateDT(s, newDataTypeInput(slug))
	r.Equal(1, v1.Version, "first create is version 1")

	v2 := tc.mustCreateDT(s, newDataTypeInput(slug))
	r.Equal(2, v2.Version, "reused slug appends version 2")
	r.NotEqual(v1.ID, v2.ID, "each version is its own row")

	v3 := tc.mustCreateDT(s, newDataTypeInput(slug))
	r.Equal(3, v3.Version)

	latest := tc.bySlug(s, slug)
	r.NotNil(latest, "bySlug resolves for a live family")
	r.Equal(v3.ID, latest.ID, "bySlug resolves to the latest live version")
	r.Equal(3, latest.Version)

	r.Equal([]int{3, 2, 1}, tc.listVersions(s, slug), "all versions listable, newest first")
}

// TestExplicitVersionAndGaps: the import path may pin an explicit version.
// Gaps above the max are honored (export preserves numbers); zero,
// negative, and hole-backfilling numbers are rejected — versions are
// strictly append-only. Auto-assignment continues from MAX, not the count.
func (s *VersioningSuite) TestExplicitVersionAndGaps() {
	r := s.Require()
	tc := s.newTenantClient()
	slug := dvSlug("gap")

	v1 := tc.mustCreateDT(s, newDataTypeInput(slug))
	r.Equal(1, v1.Version)

	pin5 := newDataTypeInput(slug)
	five := 5
	pin5.Version = &five
	v5 := tc.mustCreateDT(s, pin5)
	r.Equal(5, v5.Version, "a version above the max is honored, gap and all")

	// Backfilling the hole (2..4) is rejected: only strictly-greater
	// numbers append.
	pin3 := newDataTypeInput(slug)
	three := 3
	pin3.Version = &three
	_, err := tc.createDT(s, pin3)
	r.Error(err, "backfilling a hole must be rejected")
	r.ErrorContains(err, "greater than the current maximum")

	// Zero and negative versions fall to the same guard.
	for _, bad := range []int{0, -2} {
		in := newDataTypeInput(dvSlug("gap-bad"))
		v := bad
		in.Version = &v
		_, err := tc.createDT(s, in)
		r.Error(err, "version %d must be rejected", bad)
		r.ErrorContains(err, "greater than the current maximum")
	}

	v6 := tc.mustCreateDT(s, newDataTypeInput(slug))
	r.Equal(6, v6.Version, "auto version is MAX+1 after a pinned gap")
}

// TestImportIdempotency: re-creating an existing (slug, version) is an
// idempotent upsert — identical input returns the existing row, a
// name-only change is an in-place update, and any immutable-field change
// is a conflict.
func (s *VersioningSuite) TestImportIdempotency() {
	r := s.Require()
	tc := s.newTenantClient()
	slug := dvSlug("import")

	base := newDataTypeInput(slug)
	one := 1
	base.Version = &one
	base.JSONSchema = `{"type":"object","properties":{"a":{"type":"string"}}}`
	imp := tc.mustCreateDT(s, base)
	r.Equal(1, imp.Version)

	same, err := tc.createDT(s, base)
	r.NoError(err, "identical re-import is a no-op")
	r.Equal(imp.ID, same.ID, "identical re-import returns the existing row")
	r.Equal(1, same.Version)

	renamed := base
	newName := "Import Renamed"
	renamed.Name = &newName
	upd, err := tc.createDT(s, renamed)
	r.NoError(err, "name-only re-import updates in place")
	r.Equal(imp.ID, upd.ID)
	r.Equal("Import Renamed", upd.Name)

	conflict := base
	conflict.JSONSchema = `{"type":"object","properties":{"a":{"type":"integer"}}}`
	_, err = tc.createDT(s, conflict)
	r.Error(err, "immutable-field change on an existing version must conflict")
	r.ErrorContains(err, "different immutable fields")
}

// TestUpdateNameOnly: updateDataType changes only the name, in place — the
// row identity and version are preserved. A soft-deleted row can no longer
// be updated.
func (s *VersioningSuite) TestUpdateNameOnly() {
	r := s.Require()
	tc := s.newTenantClient()
	slug := dvSlug("rename")

	v1 := tc.mustCreateDT(s, newDataTypeInput(slug))

	updated, err := tc.renameDT(s, v1.ID, "Renamed In Place")
	r.NoError(err)
	r.Equal(v1.ID, updated.ID, "update keeps the same row")
	r.Equal("Renamed In Place", updated.Name)
	r.Equal(1, updated.Version, "rename does not bump the version")

	r.NoError(tc.deleteDT(s, v1.ID))
	_, err = tc.renameDT(s, v1.ID, "Too Late")
	r.Error(err, "cannot update a soft-deleted data type")
	r.ErrorContains(err, "not found")
}

// TestDeletePromotesPriorAndNeverReuses: deleting the latest live version
// promotes the prior one into the slug lookup; deleting the last yields
// null; recreating afterwards continues numbering above the soft-deleted
// max — a version number is never reused.
func (s *VersioningSuite) TestDeletePromotesPriorAndNeverReuses() {
	r := s.Require()
	tc := s.newTenantClient()
	slug := dvSlug("promote")

	v1 := tc.mustCreateDT(s, newDataTypeInput(slug))
	v2 := tc.mustCreateDT(s, newDataTypeInput(slug))
	v3 := tc.mustCreateDT(s, newDataTypeInput(slug))

	r.NoError(tc.deleteDT(s, v3.ID))
	latest := tc.bySlug(s, slug)
	r.NotNil(latest)
	r.Equal(v2.ID, latest.ID, "prior version promoted after deleting the latest")

	r.NoError(tc.deleteDT(s, v2.ID))
	latest = tc.bySlug(s, slug)
	r.NotNil(latest)
	r.Equal(v1.ID, latest.ID)

	r.NoError(tc.deleteDT(s, v1.ID))
	r.Nil(tc.bySlug(s, slug), "no live version left")
	r.Empty(tc.listVersions(s, slug), "soft-deleted rows are hidden from listing")

	v4 := tc.mustCreateDT(s, newDataTypeInput(slug))
	r.Equal(4, v4.Version, "recreate continues above the soft-deleted max, not reset to 1")
}

// TestPerSlugSequences: version numbering is scoped per (tenant, slug). A
// second slug starts its own sequence at 1, unaffected by another slug
// already at version 2.
func (s *VersioningSuite) TestPerSlugSequences() {
	r := s.Require()
	tc := s.newTenantClient()
	slugA := dvSlug("seq-a")
	slugB := dvSlug("seq-b")

	a1 := tc.mustCreateDT(s, newDataTypeInput(slugA))
	a2 := tc.mustCreateDT(s, newDataTypeInput(slugA))
	r.Equal(1, a1.Version)
	r.Equal(2, a2.Version)

	b1 := tc.mustCreateDT(s, newDataTypeInput(slugB))
	r.Equal(1, b1.Version, "a distinct slug starts its own sequence at 1")

	r.Len(tc.listVersions(s, slugA), 2)
	r.Len(tc.listVersions(s, slugB), 1)
}

// TestCrossTenantSequences: the same slug string in two tenants is two
// independent version sequences; each tenant resolves the slug to its own
// row, never the other tenant's.
func (s *VersioningSuite) TestCrossTenantSequences() {
	r := s.Require()
	tcA := s.newTenantClient()
	tcB := s.newTenantClient()
	slug := dvSlug("xtenant")

	a1 := tcA.mustCreateDT(s, newDataTypeInput(slug))
	a2 := tcA.mustCreateDT(s, newDataTypeInput(slug))
	r.Equal(1, a1.Version)
	r.Equal(2, a2.Version)

	b1 := tcB.mustCreateDT(s, newDataTypeInput(slug))
	r.Equal(1, b1.Version, "same slug in a different tenant starts at version 1")
	r.NotEqual(a2.ID, b1.ID)

	latestA := tcA.bySlug(s, slug)
	r.NotNil(latestA)
	r.Equal(a2.ID, latestA.ID)
	latestB := tcB.bySlug(s, slug)
	r.NotNil(latestB)
	r.Equal(b1.ID, latestB.ID)
	r.Len(tcA.listVersions(s, slug), 2)
	r.Len(tcB.listVersions(s, slug), 1)
}

// TestUnknownSlugReturnsNull: dataTypeBySlug for a slug that was never
// created resolves to null rather than erroring.
func (s *VersioningSuite) TestUnknownSlugReturnsNull() {
	tc := s.newTenantClient()
	s.Require().Nil(tc.bySlug(s, dvSlug("never-created")))
}

// TestDefaultFlag: the tenant-init `default` flag is settable on create,
// defaults to false when omitted, and is a family label orthogonal to
// version resolution.
func (s *VersioningSuite) TestDefaultFlag() {
	r := s.Require()
	tc := s.newTenantClient()

	flagged := newDataTypeInput(dvSlug("default-on"))
	yes := true
	flagged.Default = &yes
	on := tc.mustCreateDT(s, flagged)
	r.True(on.Default, "default:true is persisted")

	off := tc.mustCreateDT(s, newDataTypeInput(dvSlug("default-off")))
	r.False(off.Default, "default defaults to false")
}

// TestMalformedSchemaRejected: createDataType rejects a jsonSchema that is
// not parseable JSON — the version row must never be created.
func (s *VersioningSuite) TestMalformedSchemaRejected() {
	r := s.Require()
	tc := s.newTenantClient()
	slug := dvSlug("badschema")

	in := newDataTypeInput(slug)
	in.JSONSchema = `not-json{{`
	_, err := tc.createDT(s, in)
	r.Error(err, "unparseable jsonSchema must be rejected")

	r.Nil(tc.bySlug(s, slug), "no version row was created for the rejected schema")
}
