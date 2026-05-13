package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/state"
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

func init() {
	stateCmd.AddCommand(stateListCmd, stateMvCmd, stateRmCmd, stateShowCmd2)
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

func runStateMv(cmd *cobra.Command, args []string) error {
	from, to := args[0], args[1]
	fromType, fromName, err := splitAddr(from)
	if err != nil {
		return fmt.Errorf("from: %w", err)
	}
	toType, toName, err := splitAddr(to)
	if err != nil {
		return fmt.Errorf("to: %w", err)
	}

	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	r := s.FindResource(fromType, fromName)
	if r == nil {
		return fmt.Errorf("resource %s not found in state", from)
	}

	// Update in-place.
	for i := range s.Resources {
		if s.Resources[i].Type == fromType && s.Resources[i].Name == fromName {
			s.Resources[i].Type = toType
			s.Resources[i].Name = toName
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
	typ, name, err := splitAddr(args[0])
	if err != nil {
		return err
	}

	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	if s.FindResource(typ, name) == nil {
		return fmt.Errorf("resource %s.%s not found in state", typ, name)
	}

	s.RemoveResource(typ, name)

	if err := mgr.Save(s); err != nil {
		return err
	}
	fmt.Printf("Removed %s.%s from state\n", typ, name)
	return nil
}

func runStateShow2(cmd *cobra.Command, args []string) error {
	typ, name, err := splitAddr(args[0])
	if err != nil {
		return err
	}

	mgr := state.NewManager(flagStateFile)
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	r := s.FindResource(typ, name)
	if r == nil {
		return fmt.Errorf("resource %s.%s not found in state", typ, name)
	}

	data, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(data))
	return nil
}

func splitAddr(addr string) (typ, name string, err error) {
	parts := strings.SplitN(addr, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%q: expected type.name", addr)
	}
	return parts[0], parts[1], nil
}
