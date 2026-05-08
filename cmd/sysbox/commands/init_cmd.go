package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new sysbox workspace",
	RunE:  runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	stateDir := filepath.Dir(flagStateFile)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	fmt.Printf("Initialized sysbox workspace:\n")
	fmt.Printf("  config:  %s\n", flagConfigFile)
	fmt.Printf("  state:   %s\n", flagStateFile)
	return nil
}
