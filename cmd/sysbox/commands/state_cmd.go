package commands

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/address"
)

var stateCmd = &cobra.Command{
	Use:   "state",
	Short: "Inspect and manipulate the state file",
}

var stateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all resources currently in state",
	RunE:  runStateList,
}

var stateMvCmd = &cobra.Command{
	Use:   "mv <type.old_name> <type.new_name>",
	Short: "Rename a resource in state without destroying it",
	Args:  cobra.ExactArgs(2),
	RunE:  runStateMv,
}

var stateRmCmd = &cobra.Command{
	Use:   "rm <type.name>",
	Short: "Remove a resource from state (does not destroy the real resource)",
	Args:  cobra.ExactArgs(1),
	RunE:  runStateRm,
}

var stateShowCmd2 = &cobra.Command{
	Use:   "show <type.name>",
	Short: "Show full instance attributes of a resource",
	Args:  cobra.ExactArgs(1),
	RunE:  runStateShow2,
}

var stateGetCmd = &cobra.Command{
	Use:   "get <type.name[.attr]>",
	Short: "Print a resource instance or attribute from state",
	Args:  cobra.ExactArgs(1),
	RunE:  runStateGet,
}

func init() {
	stateCmd.AddCommand(stateListCmd, stateMvCmd, stateRmCmd, stateShowCmd2, stateGetCmd)
}

func runStateList(cmd *cobra.Command, args []string) error {
	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	if len(s.Resources) == 0 {
		fmt.Println("(no resources)")
		return nil
	}

	for _, r := range s.Resources {
		fmt.Printf("%s [provider=%s]\n", r.Address, r.Driver)
	}
	return nil
}

func runStateMv(cmd *cobra.Command, args []string) error {
	from, to := args[0], args[1]
	fromAddr, err := address.Parse(from)
	if err != nil {
		return fmt.Errorf("from: %w", err)
	}
	toAddr, err := address.Parse(to)
	if err != nil {
		return fmt.Errorf("to: %w", err)
	}

	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	r := s.FindResource(fromAddr)
	if r == nil {
		return fmt.Errorf("resource %s not found in state", from)
	}

	// Update in-place.
	for i := range s.Resources {
		if s.Resources[i].Address.Equal(fromAddr) {
			s.Resources[i].Address = toAddr
			break
		}
	}

	if err := mgr.Save(s); err != nil {
		return err
	}
	fmt.Printf("Moved %s → %s\n", from, to)
	return nil
}

func runStateRm(cmd *cobra.Command, args []string) error {
	addr, err := address.Parse(args[0])
	if err != nil {
		return err
	}

	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	if s.FindResource(addr) == nil {
		return fmt.Errorf("resource %s not found in state", addr)
	}

	s.RemoveResource(addr)

	if err := mgr.Save(s); err != nil {
		return err
	}
	fmt.Printf("Removed %s from state\n", addr)
	return nil
}

func runStateShow2(cmd *cobra.Command, args []string) error {
	addr, err := address.Parse(args[0])
	if err != nil {
		return err
	}

	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	r := s.FindResource(addr)
	if r == nil {
		return fmt.Errorf("resource %s not found in state", addr)
	}

	data, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(data))
	return nil
}

func runStateGet(cmd *cobra.Command, args []string) error {
	return printStateAddress(nil, args[0])
}
