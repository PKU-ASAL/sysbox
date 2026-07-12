package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/agentexec"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/runtime"
)

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Tear down all resources in state",
	RunE:  runDestroy,
}

func runDestroy(cmd *cobra.Command, args []string) error {
	// Destroy needs root only if the original apply needed it.
	// We re-check from the HCL file; if the file is absent or
	// NAT-only, we proceed without root.
	if root, err := tryLoadRoot(); err == nil {
		if err := checkRoot(root); err != nil {
			return err
		}
	} else if os.Getuid() != 0 {
		// No HCL file available and not root — if the state has
		// non-NAT networks, we'd fail later anyway. Try anyway;
		// Docker-only destroys succeed without root.
	}

	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if len(s.Resources) == 0 {
		fmt.Println("Nothing to destroy.")
		return nil
	}

	run := newLocalRun("destroy", localTopology())
	aborted := false
	bridge := agentexec.NewLocalBridge(agentexec.LocalOptions{
		Topology:   run.Topology,
		ConfigFile: flagConfigFile,
		StatePath:  statePath(),
		BackendURL: flagBackend,
		RunsDir:    localRunsDir(),
		BeforeDestroy: func(plan *runtime.Plan) error {
			fmt.Printf("Will destroy %d resource(s).\n", len(plan.Actions))
			if flagAutoApprove {
				return nil
			}
			ok, err := confirmPrompt("Destroy")
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
		return fmt.Errorf("destroy: %s", run.Err)
	}
	return nil
}

// tryLoadRoot parses the HCL config file without requiring a full workspace.
// Returns an error if the file doesn't exist or can't be parsed.
func tryLoadRoot() (*config.Root, error) {
	return config.ParseFile(flagConfigFile)
}
