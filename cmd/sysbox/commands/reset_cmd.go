package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/runtime"
)

var flagResetTarget string

var resetCmd = &cobra.Command{
	Use:   "reset [--target sysbox_node.name]",
	Short: "Recreate managed guests from their immutable baselines",
	Args:  cobra.NoArgs,
	RunE:  runReset,
}

func init() {
	resetCmd.Flags().StringVar(&flagResetTarget, "target", "", "reset exactly one node (sysbox_node.name)")
}

func runReset(cmd *cobra.Command, _ []string) error {
	_, _, _, root, _, err := loadWorkspaceWithRoot()
	if err != nil {
		return err
	}
	if err := checkRoot(root); err != nil {
		return err
	}

	run := newLocalRun("reset", localTopology())
	run.Target = flagResetTarget
	run.UnsafeState = flagAllowUnsafeState
	aborted := false
	bridge := agentexec.NewLocalBridge(agentexec.LocalOptions{
		Topology:         run.Topology,
		ConfigFile:       flagConfigFile,
		StatePath:        statePath(),
		BackendURL:       flagBackend,
		AllowUnsafeState: flagAllowUnsafeState,
		RunsDir:          localRunsDir(),
		Target:           flagResetTarget,
		BeforeReset: func(plan *runtime.Plan) error {
			runtime.PrintPlan(plan, true)
			if flagAutoApprove {
				return nil
			}
			ok, err := confirmPrompt("Reset")
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
		return fmt.Errorf("reset: %s", run.Err)
	}
	return nil
}
