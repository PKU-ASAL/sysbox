package commands

import (
	"context"
	"fmt"
	"io"

	"github.com/oslab/sysbox/pkg/provider/firecracker"
	"github.com/oslab/sysbox/pkg/substrate"
)

type managedVMInventory interface {
	ListManagedNodes(context.Context) ([]substrate.NodeHandle, error)
}

// findOrphanFirecracker consumes provider-verified observations. Candidates are
// scoped to known owners and exclude both live state and retained snapshots;
// neither the observation nor the reference digest authorizes a delete.
func findOrphanFirecracker(ctx context.Context, active activeIDs, inventory managedVMInventory, report io.Writer) ([]orphan, error) {
	handles, err := inventory.ListManagedNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("Firecracker inventory unavailable: %w", err)
	}
	var candidates []orphan
	for _, handle := range handles {
		hs, ok := handle.Provider.(*firecracker.HandleState)
		if !ok || hs == nil || handle.ID == "" || handle.Generation != hs.Generation {
			return nil, fmt.Errorf("Firecracker inventory contains invalid handle identity")
		}
		if !gcOwnerAllowed(active, hs.OwnerUID, hs.TopologyUID, hs.ResourceUID, hs.Generation) || active.inventories[hs.OwnerUID] == "" {
			fmt.Fprintf(report, "skipped Firecracker VM %s: durable owner is not inventoried or identity is incomplete\n", handle.ID)
			continue
		}
		if !active.firecracker[handle.ID] {
			candidates = append(candidates, orphan{Kind: "firecracker", ID: handle.ID, Topo: hs.TopologyUID, OwnerUID: hs.OwnerUID, ResourceUID: hs.ResourceUID, Generation: hs.Generation, InventoryDigest: active.inventories[hs.OwnerUID]})
		}
	}
	return candidates, nil
}
