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

var flagInitAuthorityDirectory string

func init() {
	initCmd.Flags().StringVar(&flagInitAuthorityDirectory, "authority-dir", "", "initialize a new private local signing authority directory")
}

func runInit(cmd *cobra.Command, args []string) error {
	if flagInitAuthorityDirectory != "" {
		path, err := initializeLocalAuthority(flagInitAuthorityDirectory)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Local authority initialized. Set SYSBOX_CONFIG=%s\n", path)
		fmt.Fprintf(cmd.OutOrStdout(), "Enroll guests with %s\n", filepath.Join(filepath.Dir(path), "issuer.pub"))
	}
	stateDir := filepath.Dir(flagStateFile)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	fmt.Printf("Initialized sysbox workspace:\n")
	fmt.Printf("  config:  %s\n", flagConfigFile)
	fmt.Printf("  state:   %s\n", flagStateFile)
	return nil
}
