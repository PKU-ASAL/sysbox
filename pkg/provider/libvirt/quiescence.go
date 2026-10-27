package libvirt

import (
	"context"
	"fmt"
	"github.com/oslab/sysbox/pkg/substrate"
)

// libvirt/virsh has no durable request token that can be queried after a
// controller interruption. Domain state or a returned libvirt error is not a
// stop proof; unknown mutations fail closed.
func (s *Substrate) ProveQuiescence(ctx context.Context, handle substrate.NodeHandle, operationID string, identity substrate.OperationIdentity) (substrate.QuiescenceProof, error) {
	if err := ctx.Err(); err != nil {
		return substrate.QuiescenceProof{}, err
	}
	return substrate.QuiescenceProof{}, fmt.Errorf("libvirt operation %q lacks durable daemon fencing: %w", operationID, substrate.ErrQuiescenceUnsupported)
}
