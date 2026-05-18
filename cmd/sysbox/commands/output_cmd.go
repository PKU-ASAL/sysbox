package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var outputCmd = &cobra.Command{
	Use:   "output [type.name[.attr]]",
	Short: "Print outputs from state (JSON format, or a specific attribute)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runOutput,
}

func runOutput(cmd *cobra.Command, args []string) error {
	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	if len(args) == 0 {
		bytes, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(bytes))
		return nil
	}

	parts := strings.Split(args[0], ".")
	if len(parts) < 2 {
		return fmt.Errorf("expected type.name[.attr], got %q", args[0])
	}
	r := s.FindResource(parts[0], parts[1])
	if r == nil {
		return fmt.Errorf("resource %s.%s not found", parts[0], parts[1])
	}

	if len(parts) == 2 {
		bytes, _ := json.MarshalIndent(r.Instance, "", "  ")
		fmt.Println(string(bytes))
		return nil
	}

	val, ok := r.Instance[parts[2]]
	if !ok {
		return fmt.Errorf("attribute %s not found on %s.%s", parts[2], parts[0], parts[1])
	}
	fmt.Println(val)
	return nil
}
