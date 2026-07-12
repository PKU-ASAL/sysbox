package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/address"
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

	resourceAddress, attribute, err := parseStateGetAddress(addr)
	if err != nil {
		return err
	}
	r := s.FindResource(resourceAddress)
	if r == nil {
		return fmt.Errorf("resource %s not found", resourceAddress)
	}

	if attribute == "" {
		bytes, _ := json.MarshalIndent(r.Attributes.GoValue(), "", "  ")
		fmt.Println(string(bytes))
		return nil
	}

	val, ok := r.AttributeMap()[attribute]
	if !ok {
		return fmt.Errorf("attribute %s not found on %s", attribute, resourceAddress)
	}
	fmt.Println(val)
	return nil
}

func parseStateGetAddress(input string) (address.Address, string, error) {
	if parsed, err := address.Parse(input); err == nil {
		return parsed, "", nil
	}
	index := strings.LastIndexByte(input, '.')
	if index < 0 {
		return address.Address{}, "", fmt.Errorf("expected canonical resource address[.attribute], got %q", input)
	}
	parsed, err := address.Parse(input[:index])
	if err != nil {
		return address.Address{}, "", err
	}
	if input[index+1:] == "" {
		return address.Address{}, "", fmt.Errorf("attribute is empty")
	}
	return parsed, input[index+1:], nil
}

type stateReader interface {
	FindResource(address.Address) *state.Resource
}
