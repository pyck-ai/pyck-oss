//go:build integration

package datatypeversioning

import (
	"fmt"
	"strings"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/google/uuid"

	managementapi "github.com/pyck-ai/pyck/backend/management/api"

	"github.com/pyck-ai/pyck/tests/integration/internal/fixtures"
	"github.com/pyck-ai/pyck/tests/integration/internal/gateway"
	"github.com/pyck-ai/pyck/tests/integration/tests"
)

// VersioningSuite drives the DataType versioning contract through the
// gateway with the generated management API client. Tests provision their
// own tenant via newTenantClient, so suite state stays empty.
type VersioningSuite struct {
	tests.Base
}

// tenantClient is one freshly-provisioned tenant plus a management client
// bound to its writer PAT.
type tenantClient struct {
	api      managementapi.Client
	tenantID string
}

// newTenantClient registers a fresh tenant, provisions a writer machine
// user, and returns a management client bound to it. Cleanup is deferred on
// the suite (drained after the test method finishes).
func (s *VersioningSuite) newTenantClient() *tenantClient {
	r := s.Require()
	rt, err := gateway.RegisterTenant(s.Ctx, s.Cfg, fixtures.NewTenant())
	r.NoError(err, "register tenant")
	s.DeferTenantCleanup(rt.ID)

	p, err := tests.ProvisionUserInTenant(s.Ctx, s.Cfg, s.ZConn, rt, tests.RolesWithServiceGates("writer"))
	r.NoError(err, "provision writer PAT")

	return &tenantClient{
		api:      gateway.NewClientForTenant(s.Cfg, p.PAT, rt.ID),
		tenantID: rt.ID,
	}
}

// dvSlug returns a unique slug so re-runs and parallel packages never
// collide on the (tenant, slug) family — and, defensively, neither do two
// tests that end up sharing a tenant by mistake.
func dvSlug(prefix string) string {
	return fmt.Sprintf("dv-%s-%s", prefix, strings.ToLower(gofakeit.LetterN(10)))
}

// permissiveSchema accepts any JSON object.
const permissiveSchema = `{"type":"object"}`

// strictZoneSchema requires exactly one string property "zone".
const strictZoneSchema = `{"type":"object","properties":{"zone":{"type":"string"}},"required":["zone"],"additionalProperties":false}`

// zoneFloorSchema is the "v2" evolution of strictZoneSchema: zone plus a
// required integer floor, still closed to extra properties.
const zoneFloorSchema = `{"type":"object","properties":{"zone":{"type":"string"},"floor":{"type":"integer"}},"required":["zone","floor"],"additionalProperties":false}`

// newDataTypeInput builds a create input for the slug with a permissive
// schema; tests override fields as needed.
func newDataTypeInput(slug string) managementapi.CreateDataTypeInput {
	name := "DV " + slug
	entity := "Location"
	return managementapi.CreateDataTypeInput{
		Name:       &name,
		Slug:       &slug,
		Entity:     entity,
		JSONSchema: permissiveSchema,
	}
}

// createDT publishes a version and returns the created node (or an error).
func (tc *tenantClient) createDT(s *VersioningSuite, input managementapi.CreateDataTypeInput) (*managementapi.CreateDataType_CreateDataType, error) {
	resp, err := tc.api.CreateDataType(s.Ctx, managementapi.CreateDataTypeArgs{Input: input})
	if err != nil {
		return nil, err
	}
	return resp.GetCreateDataType(), nil
}

// mustCreateDT is createDT that fails the test on error or a nil node.
func (tc *tenantClient) mustCreateDT(s *VersioningSuite, input managementapi.CreateDataTypeInput) *managementapi.CreateDataType_CreateDataType {
	node, err := tc.createDT(s, input)
	s.Require().NoError(err, "createDataType %v", input.Slug)
	s.Require().NotNil(node, "createDataType returned no node")
	return node
}

// bySlug resolves the latest live version for the slug (nil when none).
func (tc *tenantClient) bySlug(s *VersioningSuite, slug string) *managementapi.GetDataTypeBySlug_DataTypeBySlug {
	resp, err := tc.api.GetDataTypeBySlug(s.Ctx, managementapi.GetDataTypeBySlugArgs{Slug: slug})
	s.Require().NoError(err, "dataTypeBySlug %s", slug)
	return resp.GetDataTypeBySlug()
}

// listVersions returns all live versions of the slug, newest first.
func (tc *tenantClient) listVersions(s *VersioningSuite, slug string) []int {
	direction := managementapi.OrderDirectionDesc
	field := managementapi.DataTypeOrderFieldVersion
	resp, err := tc.api.GetDataTypes(s.Ctx, managementapi.GetDataTypesArgs{
		Where:   &managementapi.DataTypeWhereInput{Slug: &slug},
		OrderBy: &managementapi.DataTypeOrder{Direction: direction, Field: &field},
	})
	s.Require().NoError(err, "dataTypes where slug=%s", slug)
	var versions []int
	for _, e := range resp.GetDataTypes().GetEdges() {
		versions = append(versions, e.GetNode().Version)
	}
	return versions
}

// renameDT updates the row's name in place (the only mutable field).
func (tc *tenantClient) renameDT(s *VersioningSuite, id, name string) (*managementapi.UpdateDataType_UpdateDataType, error) {
	resp, err := tc.api.UpdateDataType(s.Ctx, managementapi.UpdateDataTypeArgs{
		Id:    id,
		Input: managementapi.UpdateDataTypeInput{Name: &name},
	})
	if err != nil {
		return nil, err
	}
	return resp.GetUpdateDataType(), nil
}

// deleteDT soft-deletes the version row.
func (tc *tenantClient) deleteDT(s *VersioningSuite, id string) error {
	_, err := tc.api.DeleteDataType(s.Ctx, managementapi.DeleteDataTypeArgs{Id: id})
	return err
}

// createLocation writes a Location pinned to the given dataTypeID (nil to
// omit the pin) with the given data payload.
func (tc *tenantClient) createLocation(s *VersioningSuite, dataTypeID *uuid.UUID, data map[string]any) (*managementapi.CreateLocation_CreateLocation_Location, error) {
	resp, err := tc.api.CreateLocation(s.Ctx, managementapi.CreateLocationArgs{
		Input: managementapi.CreateLocationInput{
			Name:       "DV-Loc-" + strings.ToLower(gofakeit.LetterN(10)),
			DataTypeID: dataTypeID,
			Data:       data,
		},
	})
	if err != nil {
		return nil, err
	}
	return resp.GetCreateLocation().GetLocation(), nil
}

// updateLocation updates a Location's pin and/or data.
func (tc *tenantClient) updateLocation(s *VersioningSuite, id string, dataTypeID *uuid.UUID, data map[string]any) (*managementapi.UpdateLocation_UpdateLocation_Location, error) {
	resp, err := tc.api.UpdateLocation(s.Ctx, managementapi.UpdateLocationArgs{
		Id: id,
		Input: managementapi.UpdateLocationInput{
			DataTypeID: dataTypeID,
			Data:       data,
		},
	})
	if err != nil {
		return nil, err
	}
	return resp.GetUpdateLocation().GetLocation(), nil
}

// mustUUID parses the string id returned by the API into a uuid.UUID.
func mustUUID(s *VersioningSuite, id string) uuid.UUID {
	u, err := uuid.Parse(id)
	s.Require().NoError(err, "parse uuid %q", id)
	return u
}
