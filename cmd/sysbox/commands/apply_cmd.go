package commands

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/spf13/cobra"
)

var (
	flagApplyRefresh bool
	flagApplyTarget  string
)

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
	g, mgr, s, root, evalCtx, err := loadWorkspaceWithRoot()
	if err != nil {
		return err
	}

	if err := checkRoot(root); err != nil {
		return err
	}

	plan, err := runtime.ComputePlan(g, s)
	if err != nil {
		return err
	}

	// --target: restrict plan to a single resource.
	if flagApplyTarget != "" {
		typ, name, err := splitAddr(flagApplyTarget)
		if err != nil {
			return fmt.Errorf("--target: %w", err)
		}
		plan = runtime.FilterPlanByTarget(plan, typ, name)
		fmt.Printf("Targeting: %s.%s\n", typ, name)
	}

	exec := runtime.NewExecutor(g, s)
	if flagApplyRefresh {
		exec.Refresh(context.Background(), plan)
	}

	if !plan.HasChanges() {
		fmt.Println("No changes. Apply is a no-op.")
		return nil
	}

	runtime.PrintPlan(plan, true)

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
	outputs, err := evaluateOutputs(root, evalCtx)
	if err != nil {
		return err
	}
	printOutputs(outputs)
	return nil
}
