package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
)

var flagApplyRefresh bool

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply the plan: provision missing resources",
	RunE:  runApply,
}

func init() {
	applyCmd.Flags().BoolVar(&flagApplyRefresh, "refresh", false, "probe existing resources for drift before applying")
}

func runApply(cmd *cobra.Command, args []string) error {
	g, mgr, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	plan, err := runtime.ComputePlan(g, s)
	if err != nil {
		return err
	}
	if !plan.HasChanges() {
		fmt.Println("No changes. Apply is a no-op.")
		return nil
	}

	exec := runtime.NewExecutor(g, s)
	if flagApplyRefresh {
		exec.Refresh(context.Background(), plan)
	}

	fmt.Println(plan.Summary())
	if err := exec.Apply(context.Background(), plan); err != nil {
		_ = mgr.Save(s)
		return fmt.Errorf("apply: %w", err)
	}

	if err := mgr.Save(s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	fmt.Println("Apply complete.")
	return nil
}
