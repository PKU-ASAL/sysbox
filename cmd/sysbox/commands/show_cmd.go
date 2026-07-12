package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show <type.name>",
	Short: "Print resource details from state as JSON",
	Args:  cobra.ExactArgs(1),
	RunE:  runShow,
}

func runShow(cmd *cobra.Command, args []string) error {
	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return err
	}

	parts := strings.SplitN(args[0], ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("expected type.name (e.g. sysbox_node.web), got %q", args[0])
	}

	r := s.FindResource(address.Resource(parts[0], parts[1]))
	if r == nil {
		return fmt.Errorf("resource %s not found in state", args[0])
	}

	bytes, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(bytes))
	return nil
}
