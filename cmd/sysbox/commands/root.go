package commands

import (
	"context"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "sysbox",
	Short: "AI 红队的 Terraform — Linux 攻防靶场 IaC",
}

var (
	flagConfigFile string
	flagStateFile  string
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagConfigFile, "file", "f",
		"field.sysbox.hcl", "path to sysbox HCL config")
	rootCmd.PersistentFlags().StringVar(&flagStateFile, "state",
		"runs/default/state.json", "path to state file")

	rootCmd.AddCommand(initCmd, planCmd, applyCmd, destroyCmd, stateCmd, showCmd, outputCmd, validateCmd)
}

// Execute is called by main(). Returns an error so main() can set exit code.
func Execute() error { return rootCmd.Execute() }

// ExecuteContext passes a context (with signal cancellation) to all commands.
func ExecuteContext(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}
