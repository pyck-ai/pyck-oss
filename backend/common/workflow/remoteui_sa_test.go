package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pyck-ai/pyck/backend/common/workflow"
)

func TestParseDeploymentVersionSA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		value      string
		deployment string
		want       workflow.DeploymentVersionRef
		ok         bool
	}{
		{name: "empty is unversioned", value: "", ok: false},
		{name: "v32 colon form", value: "wf:b1", want: workflow.DeploymentVersionRef{"wf", "b1"}, ok: true},
		{name: "v31 dot form", value: "wf.b1", want: workflow.DeploymentVersionRef{"wf", "b1"}, ok: true},
		{name: "build id may contain dots", value: "wf:1.2.3", want: workflow.DeploymentVersionRef{"wf", "1.2.3"}, ok: true},
		{
			name: "deployment name anchors a dotted name in dot form", value: "pyck.workflow.b1", deployment: "pyck.workflow",
			want: workflow.DeploymentVersionRef{"pyck.workflow", "b1"}, ok: true,
		},
		{
			name: "deployment name anchors colon form", value: "pyck.workflow:1.2.3", deployment: "pyck.workflow",
			want: workflow.DeploymentVersionRef{"pyck.workflow", "1.2.3"}, ok: true,
		},
		{name: "mismatched deployment name falls back to delimiters", value: "wf:b1", deployment: "other", want: workflow.DeploymentVersionRef{"wf", "b1"}, ok: true},
		{name: "deployment name without a build is unparseable", value: "wf", deployment: "wf", ok: false},
		{name: "no delimiter", value: "wf", ok: false},
		{name: "empty build id", value: "wf:", ok: false},
		{name: "empty deployment", value: ":b1", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := workflow.ParseDeploymentVersionSA(tt.value, tt.deployment)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
