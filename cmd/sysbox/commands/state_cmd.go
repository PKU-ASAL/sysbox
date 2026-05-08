package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/state"
)

var stateCmd = &cobra.Command{
	Use:   "state",
	Short: "Inspect the state file",
}

var stateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all resources currently in state",
	RunE:  runStateList,
}

func init() {
	stateCmd.AddCommand(stateListCmd)
}

func runStateList(cmd *cobra.Command, args []string) error {
	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	if len(s.Resources) == 0 {
		fmt.Println("(no resources)")
		return nil
	}

	for _, r := range s.Resources {
		fmt.Printf("%s.%s [provider=%s]\n", r.Type, r.Name, r.Provider)
	}
	return nil
}
