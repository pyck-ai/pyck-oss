//go:build integration

package customeventforgery_test

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/tests/integration/tests"
)

const (
	mCreateDT   = `mutation($i:CreateDataTypeInput!){createDataType(input:$i){id}}`
	mCreateItem = `mutation($i:CreateInventoryItemInput!){createInventoryItem(input:$i){inventoryItem{id dataTypeID}}}`
	mSendEvent  = `mutation($i:SendCustomEventInput!){sendCustomEvent(input:$i){success}}`

	// effectWindow bounds how long a forged event is given to reach the
	// inventory service's datatype cache. It lands within a second on a local
	// stack; the window only has to cover consumer lag.
	effectWindow = 15 * time.Second
)

// TestForgedDatatypeDeleteDoesNotBreakOtherTenant: A sends a "datatype
// delete" custom event naming B's datatype. B's writes that use the datatype
// must keep working.
func (s *CustomEventSuite) TestForgedDatatypeDeleteDoesNotBreakOtherTenant() {
	a, b := s.provision("A"), s.provision("B")
	dt := s.createItemDatatype(b)

	s.Run("positive control: B writes a typed item", func() {
		s.Require().NoError(s.createTypedItem(b, dt))
	})

	res := s.gql(a.PAT, a.RT.ID, mSendEvent, map[string]any{"i": map[string]any{
		"type": "datatype", "operation": "delete", "payload": map[string]any{"id": dt},
	}})
	s.T().Logf("A's forged datatype delete → failed=%v err=%q", res.failed(), res.errText())
	s.True(res.failed(), "sendCustomEvent accepted a custom event named after the datatype entity")
	s.Contains(res.errText(), "reserved", "forged event failed for an unrelated reason")

	s.Run("B's typed writes keep working after A's forged delete", func() {
		s.NoError(s.firstFailure(b, dt), "A's forged event removed B's datatype from the inventory cache")
	})
}

// TestForgedDatatypeCreateDoesNotHijackOtherTenant: A sends a "datatype
// create" custom event naming B's datatype. The forged entry must neither
// hide the datatype from B nor let A write records typed with it.
func (s *CustomEventSuite) TestForgedDatatypeCreateDoesNotHijackOtherTenant() {
	a, b := s.provision("A"), s.provision("B")
	dt := s.createItemDatatype(b)

	s.Run("positive control: B writes a typed item and A cannot use B's datatype", func() {
		s.Require().NoError(s.createTypedItem(b, dt))
		s.Require().Error(s.createTypedItem(a, dt), "A used B's datatype before any forged event")
	})

	res := s.gql(a.PAT, a.RT.ID, mSendEvent, map[string]any{"i": map[string]any{
		"type": "datatype", "operation": "create", "payload": map[string]any{"id": dt},
	}})
	s.T().Logf("A's forged datatype create → failed=%v err=%q", res.failed(), res.errText())
	s.True(res.failed(), "sendCustomEvent accepted a custom event named after the datatype entity")
	s.Contains(res.errText(), "reserved", "forged event failed for an unrelated reason")

	s.Run("A still cannot write records typed with B's datatype", func() {
		s.NoError(tests.PollStable(s.Ctx, effectWindow, 500*time.Millisecond, func() error {
			if s.createTypedItem(a, dt) == nil {
				return errors.New("A wrote an item typed with B's datatype")
			}
			return nil
		}), "after A's forged create event")
	})

	s.Run("B's typed writes keep working after A's forged create", func() {
		s.NoError(s.firstFailure(b, dt), "A's forged event replaced B's datatype in the inventory cache")
	})
}

// createItemDatatype creates an inventory-item datatype in t and returns its id.
func (s *CustomEventSuite) createItemDatatype(t *tenantCtx) string {
	s.T().Helper()
	d := s.ok(t.PAT, t.RT.ID, mCreateDT, map[string]any{"i": map[string]any{
		"name": "cef item", "slug": uniq("cef-dt"), "entity": "inventoryitem",
		"jsonSchema": `{"type":"object","properties":{"weight":{"type":"number"}}}`,
	}})
	id := digStr(d, "createDataType", "id")
	s.Require().NotEmpty(id)
	return id
}

// createTypedItem creates an inventory item typed with dt as t and returns the
// GraphQL error, if any.
func (s *CustomEventSuite) createTypedItem(t *tenantCtx, dt string) error {
	res := s.gql(t.PAT, t.RT.ID, mCreateItem, map[string]any{"i": map[string]any{
		"sku": uniq("cef-sku"), "dataTypeID": dt, "data": map[string]any{"weight": 1},
	}})
	if res.failed() {
		return fmt.Errorf("createInventoryItem: %s", res.errText())
	}
	return nil
}

// firstFailure repeats t's typed write for effectWindow and returns the first
// failure, or the context's error if the window was cut short. A forged event,
// if it takes effect, does so within the window.
func (s *CustomEventSuite) firstFailure(t *tenantCtx, dt string) error {
	return tests.PollStable(s.Ctx, effectWindow, 500*time.Millisecond, func() error {
		return s.createTypedItem(t, dt)
	})
}

// TestCustomEventWithItsOwnTypeStillWorks is the positive control for the
// refusal: a custom event whose type names no entity is accepted, as the
// workflows that trigger on custom events rely on.
func (s *CustomEventSuite) TestCustomEventWithItsOwnTypeStillWorks() {
	a := s.provision("A")
	d := s.ok(a.PAT, a.RT.ID, mSendEvent, map[string]any{"i": map[string]any{
		"type": "cef-custom", "operation": "create", "payload": map[string]any{"id": uuid.NewString()},
	}})
	s.Equal(true, dig(d, "sendCustomEvent", "success"))
}
