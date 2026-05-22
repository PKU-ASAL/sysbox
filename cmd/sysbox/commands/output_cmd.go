package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

var flagOutputJSON bool

var outputCmd = &cobra.Command{
	Use:   "output [name]",
	Short: "Print topology output values",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runOutput,
}

func init() {
	outputCmd.Flags().BoolVar(&flagOutputJSON, "json", false, "print outputs as JSON")
}

func runOutput(cmd *cobra.Command, args []string) error {
	_, _, _, root, evalCtx, err := loadWorkspaceWithRoot()
	if err != nil {
		return err
	}
	outputs, err := runtime.EvaluateOutputs(root, evalCtx)
	if err != nil {
		return err
	}

	if len(args) > 0 {
		name := args[0]
		if out, ok := outputs[name]; ok {
			if flagOutputJSON {
				return printOutputsJSON(map[string]runtime.OutputValue{name: out})
			}
			fmt.Println(out.Display)
			return nil
		}
		return fmt.Errorf("output %q not found", name)
	}

	if flagOutputJSON {
		return printOutputsJSON(outputs)
	}
	printOutputsHuman(outputs)
	return nil
}

func printOutputs(outputs map[string]runtime.OutputValue) {
	if len(outputs) == 0 {
		return
	}
	fmt.Println("\nOutputs:")
	printOutputsHuman(outputs)
}

func printOutputsHuman(outputs map[string]runtime.OutputValue) {
	for _, name := range runtime.SortedOutputNames(outputs) {
		out := outputs[name]
		fmt.Printf("  %-20s = %s", name, out.Display)
		if out.Description != "" {
			fmt.Printf("  # %s", out.Description)
		}
		fmt.Println()
	}
}

func printOutputsJSON(outputs map[string]runtime.OutputValue) error {
	data, err := json.MarshalIndent(outputs, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func printStateAddress(s stateReader, addr string) error {
	if s == nil {
		mgr, err := newManager()
		if err != nil {
			return err
		}
		loaded, err := mgr.Load()
		if err != nil {
			return err
		}
		s = loaded
	}

	parts := strings.SplitN(addr, ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("expected type.name[.attr], got %q", addr)
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

type stateReader interface {
	FindResource(typ, name string) *state.Resource
}
