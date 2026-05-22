package commands

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/config"
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

	// Honour lifecycle.prevent_destroy: remove protected resources from the
	// destroy list and emit a warning.
	var toDestroy, protected []state.Resource
	for _, r := range s.Resources {
		if r.LifecyclePreventDestroy() {
			protected = append(protected, r)
		} else {
			toDestroy = append(toDestroy, r)
		}
	}
	if len(protected) > 0 {
		for _, r := range protected {
			fmt.Printf("  ! %s.%s  (lifecycle.prevent_destroy = true — skipped)\n", r.Type, r.Name)
		}
	}
	fmt.Printf("Will destroy %d resource(s).\n", len(toDestroy))
	if !flagAutoApprove {
		if ok, err := confirmPrompt("Destroy"); !ok || err != nil {
			fmt.Println("Aborted.")
			return err
		}
	}

	plan := &runtime.Plan{
		Destroy:   toDestroy,
		Protected: protected,
	}
	for _, r := range toDestroy {
		plan.Actions = append(plan.Actions, runtime.PlanAction{
			Resource: r.Type + "." + r.Name,
			Type:     r.Type,
			Name:     r.Name,
			Action:   runtime.PlanActionDelete,
			Reason:   "destroy requested",
		})
	}

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

// tryLoadRoot parses the HCL config file without requiring a full workspace.
// Returns an error if the file doesn't exist or can't be parsed.
func tryLoadRoot() (*config.Root, error) {
	return config.ParseFile(flagConfigFile)
}
