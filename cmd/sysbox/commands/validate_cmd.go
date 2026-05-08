package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Parse and validate the field config without contacting any provider",
	RunE:  runValidate,
}

func runValidate(cmd *cobra.Command, args []string) error {
	root, err := config.ParseFile(flagConfigFile)
	if err != nil {
		return err
	}

	ctx := config.BuildEvalContext(root)
	g := graph.New()
	if err := buildGraph(root, g, ctx); err != nil {
		return err
	}

	order, err := g.TopoSort()
	if err != nil {
		return err
	}

	fmt.Printf("Valid. %d substrate(s), %d resource(s), apply order:\n",
		len(root.Substrates), len(order))
	for i, id := range order {
		fmt.Printf("  %d. %s\n", i+1, id)
	}
	return nil
}
