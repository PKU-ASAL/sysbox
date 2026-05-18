package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
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
	typ, name, err := splitAddr(args[0])
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}
	externalID := args[1]

	if typ != "sysbox_node" && typ != "sysbox_network" {
		return fmt.Errorf("import only supported for sysbox_node and sysbox_network, got %q", typ)
	}

	mgr, err := newManager()
	if err != nil {
		return err
	}
	s, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// Check that the resource doesn't already exist in state.
	if r := s.FindResource(typ, name); r != nil {
		return fmt.Errorf("resource %s.%s already exists in state; remove it first", typ, name)
	}

	subName := flagImportSubstrate
	if subName == "" {
		// Try to infer substrate from resource type.
		if typ == "sysbox_network" {
			subName = "network" // isolated networks
		} else {
			return fmt.Errorf("--substrate is required for sysbox_node import")
		}
	}

	sub, err := substrate.Get(subName)
	if err != nil {
		return fmt.Errorf("substrate %q not registered: %w", subName, err)
	}

	ctx := context.Background()

	switch typ {
	case "sysbox_node":
		handle, err := sub.ReadNode(ctx, externalID)
		if err != nil {
			return fmt.Errorf("import: %w", err)
		}
		inst := map[string]any{
			"container_id": handle.ID,
			"primary_ip":   handle.Net.PrimaryIP,
		}
		if blob, err := sub.MarshalProviderState(handle); err == nil && len(blob) > 0 {
			inst["provider_extra"] = string(blob)
		}
		s.AddResource(state.Resource{
			Type:     typ,
			Name:     name,
			Provider: subName,
			Instance: inst,
		})
		fmt.Printf("Imported %s.%s (id=%s)\n", typ, name, handle.ID)

	case "sysbox_network":
		// For networks, ReadNode is not applicable; check the substrate
		// for managed network support.
		return fmt.Errorf("import for sysbox_network is not yet supported; use apply instead")

	default:
		return fmt.Errorf("import not supported for resource type %q", typ)
	}

	if err := mgr.Save(s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}
