package commands

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/runtime"
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

	if addr.Type != "sysbox_node" && addr.Type != "sysbox_network" {
		return fmt.Errorf("import only supported for sysbox_node and sysbox_network, got %q", addr.Type)
	}

	mgr, err := newManager()
	if err != nil {
		return err
	}
	if err := mgr.CheckMutationSafety(); err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Check that the resource doesn't already exist in state.
	if r := s.FindResource(addr); r != nil {
		return fmt.Errorf("resource %s already exists in state; remove it first", addr)
	}

	subName := flagImportSubstrate
	if subName == "" {
		// Try to infer substrate from resource type.
		if addr.Type == "sysbox_network" {
			subName = "network" // isolated networks
		} else {
			return fmt.Errorf("--substrate is required for sysbox_node import")
		}
	}

	ctx := context.Background()

	switch addr.Type {
	case "sysbox_node":
		handler, ok := runtime.GetResourceHandler(addr.Type)
		if !ok {
			return fmt.Errorf("resource handler %q not registered", addr.Type)
		}
		importer, ok := handler.(runtime.ResourceImporter)
		if !ok {
			return fmt.Errorf("resource %s does not support import", addr.Type)
		}
		resource, err := importer.Import(ctx, addr, subName, externalID)
		if err != nil {
			return fmt.Errorf("import: %w", err)
		}
		s.AddResource(resource)
		fmt.Printf("Imported %s (id=%s)\n", addr, resource.ExternalID)

	case "sysbox_network":
		// For networks, ReadNode is not applicable; check the substrate
		// for managed network support.
		return fmt.Errorf("import for sysbox_network is not yet supported; use apply instead")

	default:
		return fmt.Errorf("import not supported for resource type %q", addr.Type)
	}

	if err := mgr.Save(s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}
