package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/oslab/sysbox/pkg/config"
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
	outputs, err := evaluateOutputs(root, evalCtx)
	if err != nil {
		return err
	}

	if len(args) > 0 {
		name := args[0]
		if out, ok := outputs[name]; ok {
			if flagOutputJSON {
				return printOutputsJSON(map[string]OutputValue{name: out})
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

type OutputValue struct {
	Name        string    `json:"-"`
	Value       any       `json:"value"`
	Type        string    `json:"type"`
	Description string    `json:"description,omitempty"`
	Display     string    `json:"-"`
	Raw         cty.Value `json:"-"`
}

func evaluateOutputs(root *config.Root, ctx *hcl.EvalContext) (map[string]OutputValue, error) {
	out := map[string]OutputValue{}
	if root == nil {
		return out, nil
	}
	for _, block := range root.Outputs {
		val := cty.NilVal
		display := "(unevaluated)"
		var native any
		typeName := "dynamic"
		if block.Value != nil {
			v, diags := block.Value.Value(ctx)
			if diags.HasErrors() {
				return nil, fmt.Errorf("output %s: %s", block.Name, diags.Error())
			}
			val = v
			display = outputDisplayValue(v)
			native = outputNativeValue(v)
			typeName = v.Type().FriendlyName()
		}
		out[block.Name] = OutputValue{
			Name:        block.Name,
			Value:       native,
			Type:        typeName,
			Description: block.Description,
			Display:     display,
			Raw:         val,
		}
	}
	return out, nil
}

func printOutputs(outputs map[string]OutputValue) {
	if len(outputs) == 0 {
		return
	}
	fmt.Println("\nOutputs:")
	printOutputsHuman(outputs)
}

func printOutputsHuman(outputs map[string]OutputValue) {
	for _, name := range sortedOutputNames(outputs) {
		out := outputs[name]
		fmt.Printf("  %-20s = %s", name, out.Display)
		if out.Description != "" {
			fmt.Printf("  # %s", out.Description)
		}
		fmt.Println()
	}
}

func printOutputsJSON(outputs map[string]OutputValue) error {
	data, err := json.MarshalIndent(outputs, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func sortedOutputNames(outputs map[string]OutputValue) []string {
	names := make([]string, 0, len(outputs))
	for name := range outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func outputDisplayValue(v cty.Value) string {
	if !v.IsKnown() {
		return "(unknown)"
	}
	if v.IsNull() {
		return "null"
	}
	if v.Type() == cty.String {
		return v.AsString()
	}
	data, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return v.GoString()
	}
	return string(data)
}

func outputNativeValue(v cty.Value) any {
	if !v.IsKnown() || v.IsNull() {
		return nil
	}
	if v.Type() == cty.String {
		return v.AsString()
	}
	if v.Type() == cty.Bool {
		return v.True()
	}
	if v.Type() == cty.Number {
		f, _ := v.AsBigFloat().Float64()
		return f
	}
	data, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return v.GoString()
	}
	var native any
	if err := json.Unmarshal(data, &native); err != nil {
		return string(data)
	}
	return native
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
