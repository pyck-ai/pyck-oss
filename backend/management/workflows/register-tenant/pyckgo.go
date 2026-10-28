package registertenant

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/pyck-ai/pyck/backend/common/authn"
	"github.com/pyck-ai/pyck/backend/common/feature"
	commonworkflow "github.com/pyck-ai/pyck/backend/common/workflow"

	"github.com/pyck-ai/pyck/backend/management/core"
	ent "github.com/pyck-ai/pyck/backend/management/ent/gen"
)

// pyck-go is the one tenant flavour with a platform-managed worker: created
// at registration, stopped and resumed by the disable/restore workflows.
const (
	pyckGoFlavour = "pyck-go"
	// pyckGoExtension is what worker-api knows the worker as.
	pyckGoExtension = "pyckGo"
	// pyckGoDeploymentPrefix is not derived from pyckGoExtension: a deployment
	// name is a DNS-1123 subdomain, which forbids the capital in "pyckGo".
	pyckGoDeploymentPrefix = "pyck-go"

	// SkipNotPyckGo and SkipLifecycleDisabled are what the lifecycle
	// activities record when they skip the worker step: the tenant has no
	// platform-managed worker, or the step is switched off by config.
	SkipNotPyckGo         = "not-pyck-go"
	SkipLifecycleDisabled = "lifecycle-disabled"

	// AppErrorTypeTenantNotFound is the non-retryable application-error type
	// for ErrTenantNotFound: a lifecycle workflow for a tenant row that does
	// not exist cannot be fixed by retrying.
	AppErrorTypeTenantNotFound = "TenantNotFound"
)

// ErrTenantNotFound is returned by LookupTenantWorker when the tenant row
// does not exist, soft-deleted rows included.
var ErrTenantNotFound = errors.New("tenant not found")

// IsPyckGo reports whether tenant data describes a pyck-go tenant.
func IsPyckGo(data map[string]any) bool {
	return core.DetectFlavour(data) == pyckGoFlavour
}

// PyckGoDeploymentName is the worker-api name of a pyck-go tenant's worker.
func PyckGoDeploymentName(temporalNamespace string) string {
	return pyckGoDeploymentPrefix + "-" + temporalNamespace
}

// TenantWorker is a tenant's platform-managed worker as the lifecycle
// activities see it: the worker-api deployment name, and whether the tenant
// is disabled (deleted_at set) at lookup time. Activities check Disabled
// right before acting: an activity dispatched by a workflow the trigger has
// since terminated keeps running, and must not undo the newer intent.
type TenantWorker struct {
	Deployment string
	Disabled   bool
}

// LookupTenantWorker returns the tenant's platform-managed worker, or
// ok=false when the tenant has none (not pyck-go). Soft-deleted rows are
// included: disable runs after deleted_at is set.
func LookupTenantWorker(ctx context.Context, db *ent.Client, nsGetter commonworkflow.NamespaceGetter, tenantID uuid.UUID) (tw TenantWorker, ok bool, err error) {
	sysCtx := feature.Context(authn.Context(ctx, authn.SystemUser()), feature.FEATURE_SHOW_DELETED)

	row, err := db.Tenant.Get(sysCtx, tenantID)
	if ent.IsNotFound(err) {
		return TenantWorker{}, false, fmt.Errorf("%w: %s", ErrTenantNotFound, tenantID)
	}
	if err != nil {
		return TenantWorker{}, false, fmt.Errorf("load tenant %s: %w", tenantID, err)
	}
	if !IsPyckGo(row.Data) {
		return TenantWorker{}, false, nil
	}

	return TenantWorker{
		Deployment: PyckGoDeploymentName(nsGetter.GetNamespace(tenantID)),
		Disabled:   !row.DeletedAt.IsZero(),
	}, true, nil
}
