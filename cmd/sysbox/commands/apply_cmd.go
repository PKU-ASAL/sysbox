package commands

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/runtime"
)

var (
	flagApplyRefresh bool
	flagApplyTarget  string
)

var errCommandAborted = errors.New("aborted")

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply the plan: provision missing resources",
	RunE:  runApply,
}

func init() {
	applyCmd.Flags().BoolVar(&flagApplyRefresh, "refresh", false, "probe existing resources for drift before applying")
	applyCmd.Flags().StringVar(&flagApplyTarget, "target", "", "apply only this resource (type.name)")
}

func runApply(cmd *cobra.Command, args []string) error {
	_, _, _, root, evalCtx, err := loadWorkspaceWithRoot()
	if err != nil {
		return err
	}

	if err := checkRoot(root); err != nil {
		return err
	}

	run := newLocalRun("apply", localTopology())
	aborted := false
	bridge := agentexec.NewLocalBridge(agentexec.LocalOptions{
		Topology:   run.Topology,
		ConfigFile: flagConfigFile,
		StatePath:  statePath(),
		BackendURL: flagBackend,
		RunsDir:    localRunsDir(),
		Refresh:    flagApplyRefresh,
		Target:     flagApplyTarget,
		BeforeApply: func(plan *runtime.Plan) error {
			runtime.PrintPlan(plan, true)
			if flagAutoApprove {
				return nil
			}
			ok, err := confirmPrompt("Apply")
			if err != nil {
				fmt.Println("Aborted.")
				return err
			}
			if !ok {
				aborted = true
				fmt.Println("Aborted.")
				return errCommandAborted
			}
			return nil
		},
	})
	agentexec.NewExecutorWithBridge(bridge).Execute(run)
	if aborted {
		return nil
	}
	if run.Err != "" {
		return fmt.Errorf("apply: %s", run.Err)
	}
	outputs, err := runtime.EvaluateOutputs(root, evalCtx)
	if err != nil {
		return err
	}
	printOutputs(outputs)
	return nil
}

func newLocalRun(op, topology string) *controlplane.Run {
	now := time.Now().UTC()
	id := uuid.New().String()
	return &controlplane.Run{
		ID:         id,
		ProjectID:  controlplane.DefaultProjectID,
		Workspace:  topology,
		Topology:   topology,
		Op:         op,
		Status:     controlplane.RunRunning,
		AgentID:    agentexec.DefaultAgentID,
		LeaseOwner: fmt.Sprintf("sysbox-cli:%s:%s", op, id),
		QueuedAt:   now,
		AssignedAt: now,
		StartedAt:  now,
	}
}
