package api_test

import (
	"encoding/json"
	"testing"

	"github.com/gqlgo/gqlgenc/graphqljson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/workflow/api"
)

// The server sends a UInt64 as a JSON number. The generated client types must
// accept that: graphqljson.UnmarshalData decodes a response field by field (in
// the order of the struct's JSON keys), so a field it cannot decode stops the
// decode and every field after it stays empty. The routing entry's sequence
// used to be typed string, which hid the entry's targets and tenant.

func TestGeneratedClient_DecodesRoutingEntryWithNumericSequence(t *testing.T) {
	t.Parallel()

	data := json.RawMessage(`{"transactionRouting":{"transactionID":"tx-1","entries":[{
		"eventID":"e1","outcome":"DONE","recordedAt":"2026-10-01T00:00:00Z","sequence":18446744073709551615,
		"targets":[{"kind":"STARTED","workflowID":"wf_1","runID":"run-1"},{"kind":"SIGNALLED","signal":"S"}],
		"tenantID":"t1"}]}}`)

	var got api.GetTransactionRouting

	require.NoError(t, graphqljson.UnmarshalData(data, &got))
	require.Len(t, got.TransactionRouting.Entries, 1)

	entry := got.TransactionRouting.Entries[0]
	assert.Equal(t, "e1", entry.EventID)
	assert.Equal(t, uint64(18446744073709551615), entry.Sequence, "the full uint64 range survives")
	assert.Equal(t, "t1", entry.TenantID, "fields after the sequence are decoded")
	require.Len(t, entry.Targets, 2, "the targets are decoded")
	assert.Equal(t, "wf_1", *entry.Targets[0].WorkflowID)
}

func TestGeneratedClient_DecodesUserDataInputWithNumericCounters(t *testing.T) {
	t.Parallel()

	data := json.RawMessage(`{"currentUserDataInput":{"activityIndex":3,"activityCount":7,"errors":["x"]}}`)

	var got api.GetCurrentUserDataInput

	require.NoError(t, graphqljson.UnmarshalData(data, &got))
	require.NotNil(t, got.CurrentUserDataInput)
	assert.EqualValues(t, 3, got.CurrentUserDataInput.ActivityIndex)
	require.NotNil(t, got.CurrentUserDataInput.ActivityCount)
	assert.EqualValues(t, 7, *got.CurrentUserDataInput.ActivityCount)
	assert.Equal(t, []string{"x"}, got.CurrentUserDataInput.Errors, "fields after the counters are decoded")
}
