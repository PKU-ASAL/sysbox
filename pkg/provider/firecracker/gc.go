package firecracker

import (
	"context"
	"fmt"

	"github.com/oslab/sysbox/pkg/driver"
)

// DeleteManagedExternal is the Firecracker provider primitive used by the
// fenced host GC coordinator. It discovers the durable v2 manifest, verifies
// every ownership coordinate, and then uses the normal manifest destroy path.
// A missing manifest is reported as not found so the coordinator can perform
// its independent absence observation; ambiguous or incomplete evidence fails
// closed.
func (s *Substrate) DeleteManagedExternal(ctx context.Context, externalID, ownerUID, resourceUID string, generation int64) error {
	m, _, err := s.findAdoptionManifest(ctx, externalID)
	if err != nil {
		return err
	}
	if m.OwnerUID != ownerUID || m.ResourceUID != resourceUID || m.Generation != generation || m.VMID != externalID {
		return fmt.Errorf("firecracker GC ownership identity changed")
	}
	if m.Handle.OwnerUID != ownerUID || m.Handle.ResourceUID != resourceUID || m.Handle.Generation != generation {
		return fmt.Errorf("firecracker GC manifest handle identity changed")
	}
	if m.Version != manifestVersion {
		return driver.Wrap(driver.ErrorUnsupported, "firecracker", "GC requires manifest v2", nil)
	}
	return s.DestroyNode(ctx, manifestHandle(&m))
}
