package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Show changes sysbox would make without applying",
	RunE:  runPlan,
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

	fmt.Println(plan.Summary())
	for _, id := range plan.Add {
		fmt.Printf("  + %s\n", id)
	}
	for _, r := range plan.Destroy {
		fmt.Printf("  - %s.%s\n", r.Type, r.Name)
	}
	for _, id := range plan.Unchanged {
		fmt.Printf("    %s (unchanged)\n", id)
	}
	return nil
}
