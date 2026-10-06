package resolvers_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	"github.com/pyck-ai/pyck/backend/common/request"
	"github.com/pyck-ai/pyck/backend/common/test/resolver"
	"github.com/pyck-ai/pyck/backend/common/txid"

	ent "github.com/pyck-ai/pyck/backend/workflow/ent/gen"
	entworkflowsignal "github.com/pyck-ai/pyck/backend/workflow/ent/gen/workflowsignal"
	"github.com/pyck-ai/pyck/backend/workflow/ent/schema"
)

// writerA has the least role allowed to write signals.
var writerA = &authn.User{
	ID:       uuid.MustParse("6e0f3d0c-4f0a-4c1e-9d55-6c1a0b8f1500"),
	TenantID: resolver.TenantA,
	Roles:    map[uuid.UUID]authn.Role{resolver.TenantA: authn.ROLE_WRITER},
}

// systemCtx acts as the system user, which bypasses the tenant privacy filter.
func (te *testEnv) systemCtx(tenantID uuid.UUID) context.Context {
	te.t.Helper()
	return request.Context(te.t.Context(), authn.SystemUser(), tenantID)
}

// inTx runs fn in a transaction, with the tx and a txid in ctx as gqltx does.
func (te *testEnv) inTx(ctx context.Context, fn func(ctx context.Context, tx *ent.Tx) error) error {
	te.t.Helper()
	return te.withTx(ctx, func(tx *ent.Tx) error {
		return fn(ent.NewTxContext(txid.With(ctx, txid.New()), tx), tx)
	})
}

func (te *testEnv) createSignal(ctx context.Context, workflowID uuid.UUID, topic string) (*ent.WorkflowSignal, error) {
	te.t.Helper()
	var sig *ent.WorkflowSignal
	err := te.inTx(ctx, func(ctx context.Context, tx *ent.Tx) error {
		var err error
		sig, err = tx.WorkflowSignal.Create().
			SetWorkflowID(workflowID).
			SetNatsTopic(topic).
			SetTemporalSignalType(entworkflowsignal.TemporalSignalTypeIntermediate).
			SetTemporalSignal("OrderCreated").
			SetWorkerID("worker-a").
			SetExpiresAt(time.Now().UTC().Add(time.Hour)).
			Save(ctx)
		return err
	})
	return sig, err
}

// signalsOf lists workflowID's signals across all tenants.
func (te *testEnv) signalsOf(workflowID uuid.UUID) []*ent.WorkflowSignal {
	te.t.Helper()
	return te.Ent.WorkflowSignal.Query().
		Where(entworkflowsignal.WorkflowIDEQ(workflowID)).
		AllX(te.systemCtx(resolver.TenantA))
}

// TestWorkflowSignalWorkflowOwnership covers #1500: a signal may only reference
// a workflow of its own tenant.
func TestWorkflowSignalWorkflowOwnership(t *testing.T) {
	t.Parallel()

	// seed gives tenants A and B one workflow with one signal each.
	seed := func(t *testing.T) (te *testEnv, wfA, wfB *ent.Workflow, sigA, sigB *ent.WorkflowSignal) {
		t.Helper()
		te = setup(t)
		t.Cleanup(func() { te.Close(t) })

		wfA = te.newWorkflow(te.ctx(userA), userA).Create()
		wfB = te.newWorkflow(te.ctx(userB), userB).Create()

		var err error
		sigA, err = te.createSignal(te.ctx(userA), wfA.ID, "xt.topic.a")
		require.NoError(t, err)
		sigB, err = te.createSignal(te.ctx(userB), wfB.ID, "xt.topic.b")
		require.NoError(t, err)
		return te, wfA, wfB, sigA, sigB
	}

	t.Run("create in own tenant succeeds", func(t *testing.T) {
		t.Parallel()
		te, wfA, _, _, _ := seed(t)

		sig, err := te.createSignal(te.ctx(writerA), wfA.ID, "xt.topic.own")
		require.NoError(t, err)
		assert.Equal(t, tenantA, sig.TenantID)
		assert.Len(t, te.signalsOf(wfA.ID), 2)
	})

	t.Run("create referencing another tenant's workflow is refused", func(t *testing.T) {
		t.Parallel()
		te, _, wfB, _, _ := seed(t)

		_, err := te.createSignal(te.ctx(writerA), wfB.ID, "xt.topic.stolen")
		require.ErrorIs(t, err, schema.ErrWorkflowNotInTenant)

		sigs := te.signalsOf(wfB.ID)
		require.Len(t, sigs, 1, "tenant B's workflow must keep only its own signal")
		assert.Equal(t, tenantB, sigs[0].TenantID)
	})

	t.Run("system user cannot cross tenants either", func(t *testing.T) {
		t.Parallel()
		te, _, wfB, _, _ := seed(t)

		_, err := te.createSignal(te.systemCtx(tenantA), wfB.ID, "xt.topic.stolen")
		require.ErrorIs(t, err, schema.ErrWorkflowNotInTenant)
		assert.Len(t, te.signalsOf(wfB.ID), 1)
	})

	t.Run("system user in the workflow's tenant succeeds", func(t *testing.T) {
		t.Parallel()
		te, _, wfB, _, _ := seed(t)

		sig, err := te.createSignal(te.systemCtx(tenantB), wfB.ID, "xt.topic.system")
		require.NoError(t, err)
		assert.Equal(t, tenantB, sig.TenantID)
	})

	t.Run("create referencing a missing workflow is refused", func(t *testing.T) {
		t.Parallel()
		te, _, _, _, _ := seed(t)

		_, err := te.createSignal(te.ctx(writerA), uuid.New(), "xt.topic.ghost")
		require.ErrorIs(t, err, schema.ErrWorkflowNotInTenant)
	})

	t.Run("update-one re-pointing to another tenant's workflow is refused", func(t *testing.T) {
		t.Parallel()
		te, wfA, wfB, sigA, _ := seed(t)

		err := te.inTx(te.ctx(writerA), func(ctx context.Context, tx *ent.Tx) error {
			return tx.WorkflowSignal.UpdateOneID(sigA.ID).SetWorkflowID(wfB.ID).Exec(ctx)
		})
		require.ErrorIs(t, err, schema.ErrWorkflowNotInTenant)
		assert.Len(t, te.signalsOf(wfA.ID), 1)
		assert.Len(t, te.signalsOf(wfB.ID), 1)
	})

	t.Run("update-one via the generated input is refused", func(t *testing.T) {
		t.Parallel()
		te, _, wfB, sigA, _ := seed(t)

		err := te.inTx(te.ctx(writerA), func(ctx context.Context, tx *ent.Tx) error {
			return tx.WorkflowSignal.UpdateOneID(sigA.ID).
				SetInput(ent.UpdateWorkflowSignalInput{WorkflowID: &wfB.ID}).
				Exec(ctx)
		})
		require.ErrorIs(t, err, schema.ErrWorkflowNotInTenant)
		assert.Len(t, te.signalsOf(wfB.ID), 1)
	})

	t.Run("bulk update re-pointing to another tenant's workflow is refused", func(t *testing.T) {
		t.Parallel()
		te, _, wfB, _, _ := seed(t)

		// The event hook (client.Use) runs before the schema hook and refuses an
		// unsuppressed bulk write that matches rows, so suppress events to reach
		// the ownership guard this subtest is about.
		err := te.inTx(te.ctx(writerA), func(ctx context.Context, tx *ent.Tx) error {
			ctx = feature.Context(ctx, feature.FEATURE_SUPPRESS_EVENTS)
			return tx.WorkflowSignal.Update().SetWorkflowID(wfB.ID).Exec(ctx)
		})
		require.ErrorIs(t, err, schema.ErrWorkflowNotInTenant)
		assert.Len(t, te.signalsOf(wfB.ID), 1)
	})

	t.Run("update-one within own tenant succeeds", func(t *testing.T) {
		t.Parallel()
		te, wfA, _, sigA, _ := seed(t)
		wfA2 := te.newWorkflow(te.ctx(userA), userA).Create()

		err := te.inTx(te.ctx(writerA), func(ctx context.Context, tx *ent.Tx) error {
			return tx.WorkflowSignal.UpdateOneID(sigA.ID).SetWorkflowID(wfA2.ID).Exec(ctx)
		})
		require.NoError(t, err)
		assert.Empty(t, te.signalsOf(wfA.ID))
		assert.Len(t, te.signalsOf(wfA2.ID), 1)
	})

	t.Run("update not touching workflow_id skips the check", func(t *testing.T) {
		t.Parallel()
		te, _, _, sigA, _ := seed(t)

		err := te.inTx(te.ctx(writerA), func(ctx context.Context, tx *ent.Tx) error {
			return tx.WorkflowSignal.UpdateOneID(sigA.ID).SetNatsTopic("xt.topic.renamed").Exec(ctx)
		})
		require.NoError(t, err)
	})

	// ent's O2M add only claims rows with a NULL FK, so this needs no hook.
	t.Run("adding another tenant's signal from the workflow side is refused", func(t *testing.T) {
		t.Parallel()
		te, wfA, wfB, _, sigB := seed(t)

		err := te.inTx(te.ctx(writerA), func(ctx context.Context, tx *ent.Tx) error {
			return tx.Workflow.UpdateOneID(wfA.ID).AddWorkflowSignalIDs(sigB.ID).Exec(ctx)
		})
		require.True(t, ent.IsConstraintError(err), "want sqlgraph's already-connected refusal, got %v", err)
		assert.Len(t, te.signalsOf(wfA.ID), 1)
		assert.Len(t, te.signalsOf(wfB.ID), 1)
	})
}
