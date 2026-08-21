package agentexec

import (
	"testing"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
)

func TestAgentRefreshesStoredPlanBeforeApply(t *testing.T) {
	require.True(t, refreshApplyPlan("stored-plan", nil))
}

type refreshApplyHookStub bool

func (h refreshApplyHookStub) FilterApplyPlan(plan *runtime.Plan) (*runtime.Plan, error) {
	return plan, nil
}
func (h refreshApplyHookStub) RefreshApply() bool              { return bool(h) }
func (h refreshApplyHookStub) BeforeApply(*runtime.Plan) error { return nil }

func TestExplicitApplyHookControlsRefresh(t *testing.T) {
	require.False(t, refreshApplyPlan("stored-plan", refreshApplyHookStub(false)))
	require.True(t, refreshApplyPlan("stored-plan", refreshApplyHookStub(true)))
}
