package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
)

var (
	flagApplyRefresh  bool
	flagAutoApprove   bool
)

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply the plan: provision missing resources",
	RunE:  runApply,
}

func init() {
	applyCmd.Flags().BoolVar(&flagApplyRefresh, "refresh", false, "probe existing resources for drift before applying")
	applyCmd.Flags().BoolVar(&flagAutoApprove, "auto-approve", false, "skip interactive confirmation prompt")
}

func runApply(cmd *cobra.Command, args []string) error {
	requireRoot()

	g, mgr, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	plan, err := runtime.ComputePlan(g, s)
	if err != nil {
		return err
	}

	exec := runtime.NewExecutor(g, s)
	if flagApplyRefresh {
		exec.Refresh(context.Background(), plan)
	}

	if !plan.HasChanges() {
		fmt.Println("No changes. Apply is a no-op.")
		return nil
	}

	fmt.Println(plan.Summary())
	for _, id := range plan.Add {
		fmt.Printf("  + %s\n", id)
	}
	for _, id := range plan.Change {
		fmt.Printf("  ~ %s (drifted)\n", id)
	}
	for _, r := range plan.Destroy {
		fmt.Printf("  - %s.%s\n", r.Type, r.Name)
	}

	if !flagAutoApprove {
		if ok, err := confirmPrompt("Apply"); !ok || err != nil {
			fmt.Println("Aborted.")
			return err
		}
	}

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
