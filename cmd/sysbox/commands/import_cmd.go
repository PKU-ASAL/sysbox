package commands

import (
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/spf13/cobra"
)

var (
	flagImportSubstrate string
)

var importCmd = &cobra.Command{
	Use:   "import <resource_type.name> <external_id>",
	Short: "Import an existing node into sysbox state",
	Long: `Import an existing node (container, VM) into sysbox state so that
sysbox can manage it. The node must already exist in the substrate.

Example:
  sysbox import sysbox_node.db my-container-name --substrate docker
  sysbox import sysbox_node.kvm my-vm-domain --substrate libvirt`,
	Args: cobra.ExactArgs(2),
	RunE: runImport,
}

func init() {
	importCmd.Flags().StringVar(&flagImportSubstrate, "substrate", "", "substrate name (docker, libvirt, etc.)")
}

func runImport(cmd *cobra.Command, args []string) error {
	addr, err := address.Parse(args[0])
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	externalID := args[1]

	_ = addr
	_ = externalID
	_ = flagImportSubstrate
	return fmt.Errorf("local import is removed; use the API adoption plan and confirm workflow")
}
