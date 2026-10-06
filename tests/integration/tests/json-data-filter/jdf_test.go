//go:build integration

package jsondatafilter_test

import (
	"strings"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// DataFilterSuite provisions one fresh tenant with a writer.
type DataFilterSuite struct {
	tests.Base
	tenant string
	pat    string
}

// malformedFilters are data filters whose shape the operator cannot use.
var malformedFilters = []struct{ name, filter string }{
	{"Data/path only", `Data: ["type"]`},
	{"Data/empty", `Data: []`},
	{"Data/extra element", `Data: ["type", "custom", "surplus"]`},
	{"DataIn/path only", `DataIn: ["type"]`},
	{"DataIn/empty", `DataIn: []`},
	{"DataContains/path only", `DataContains: ["meta.tags"]`},
	{"DataContains/extra element", `DataContains: ["meta.tags", "a", "b"]`},
	{"DataHasKey/empty path", `DataHasKey: ""`},
}

// TestMalformedFiltersAreRefused seeds two rows per service, one of which the
// well-formed filter matches, and expects every malformed filter to be refused
// instead of returning both rows.
func (s *DataFilterSuite) TestMalformedFiltersAreRefused() {
	s.tenant, s.pat = s.provision()

	for _, tgt := range serviceTargets() {
		s.Run(tgt.service, func() {
			dt := s.createDataType(tgt)
			s.seed(tgt, dt, map[string]any{"type": "custom", "meta": map[string]any{"tags": []any{"a"}}}, true)
			s.seed(tgt, dt, map[string]any{"type": "other", "meta": map[string]any{"tags": []any{"b"}}}, false)

			all := s.count(tgt.field, `{ dataTypeID: "`+dt+`" }`)
			s.Require().Equal(2, all, "%s: both seeded rows must be listed", tgt.service)
			s.Require().Equal(1, s.count(tgt.field, `{ dataTypeID: "`+dt+`", Data: ["type", "custom"] }`),
				"%s: the well-formed filter must match one row", tgt.service)

			for _, mf := range malformedFilters {
				s.Run(mf.name, func() {
					// In the same object as the dataTypeID filter: a dropped
					// data filter leaves that one, so both seeded rows return.
					where := `{ dataTypeID: "` + dt + `", ` + mf.filter + ` }`
					call := s.post(s.pat, s.tenant, filterQuery(tgt.field, where), nil)
					s.Require().NotEmptyf(call.errors,
						"%s %s: the filter was dropped and the query returned %d of the tenant's %d rows",
						tgt.service, mf.name, s.totalCount(call, tgt.field), all)
					s.Contains(strings.Join(call.errors, " | "), "invalid JSON data filter")
					s.Zero(s.totalCount(call, tgt.field), "a refused filter must return no rows")
				})
			}
		})
	}
}
