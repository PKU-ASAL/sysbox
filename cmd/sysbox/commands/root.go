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
	flagConfigFile       string
	flagStateFile        string
	flagBackend          string
	flagAutoApprove      bool
	flagAllowUnsafeState bool
)

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagConfigFile, "file", "f",
		"field.sysbox.hcl", "path to sysbox HCL config")
	rootCmd.PersistentFlags().StringVar(&flagStateFile, "state",
		".sysbox/runs/default/state.json", "path to state file (or URL for remote backend)")
	rootCmd.PersistentFlags().StringVar(&flagBackend, "backend", "",
		"state backend URL (s3://bucket/key, https://host/path); overrides --state")
	rootCmd.PersistentFlags().BoolVar(&flagAutoApprove, "auto-approve",
		false, "skip interactive confirmation prompt")
	rootCmd.PersistentFlags().BoolVar(&flagAllowUnsafeState, "allow-unsafe-state",
		false, "allow mutation with a backend that lacks locking or CAS")

	rootCmd.AddCommand(initCmd, planCmd, applyCmd, destroyCmd, stateCmd, showCmd, outputCmd, validateCmd, serveCmd,
		pauseCmd, resumeCmd, resetCmd, importCmd)
}

// Execute is called by main(). Returns an error so main() can set exit code.
func Execute() error { return rootCmd.Execute() }

// ExecuteContext passes a context (with signal cancellation) to all commands.
func ExecuteContext(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}
