package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

var destroyCmd = &cobra.Command{
	Use:   "destroy",
	Short: "Tear down all resources in state",
	RunE:  runDestroy,
}

func runDestroy(cmd *cobra.Command, args []string) error {
	requireRoot()
	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	if len(s.Resources) == 0 {
		fmt.Println("Nothing to destroy.")
		return nil
	}

	fmt.Printf("Will destroy %d resource(s).\n", len(s.Resources))
	if !flagAutoApprove {
		if ok, err := confirmPrompt("Destroy"); !ok || err != nil {
			fmt.Println("Aborted.")
			return err
		}
	}

	plan := &runtime.Plan{Destroy: append([]state.Resource(nil), s.Resources...)}

	exec := runtime.NewExecutor(graph.New(), s)
	if err := exec.Destroy(context.Background(), plan); err != nil {
		_ = mgr.Save(s)
		return fmt.Errorf("destroy: %w", err)
	}

	if err := mgr.Save(s); err != nil {
		return err
	}
	fmt.Println("Destroy complete.")
	return nil
}
