package commands

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Show changes sysbox would make without applying",
	RunE:  runPlan,
}

var flagRefresh bool

func init() {
	planCmd.Flags().BoolVar(&flagRefresh, "refresh", false, "probe existing resources for drift")
}

func runPlan(cmd *cobra.Command, args []string) error {
	g, _, s, err := loadWorkspace()
	if err != nil {
		return err
	}

	plan, err := runtime.ComputePlan(g, s)
	if err != nil {
		return err
	}

	if flagRefresh {
		runtime.NewExecutor(g, s).Refresh(context.Background(), plan)
	}

	runtime.PrintPlan(plan, false)
	return nil
}
