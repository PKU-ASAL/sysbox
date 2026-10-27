package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestOwnershipPreparationRequiresStableCanonicalFreeze(t *testing.T) {
	p := controlplane.OwnershipTransferPlan{Version: 1, ID: "transfer", SourceTopology: "source", TargetTopology: "target", SourceAddress: "sysbox_node.old", TargetAddress: "sysbox_node.new", AgentID: "host", Provider: "libvirt", ExternalID: "external", Source: substrate.OperationIdentity{ActionID: "old", OwnerUID: "source", TopologyUID: "source", ResourceUID: "old", Generation: 1}, Target: substrate.OperationIdentity{ActionID: "transfer", OwnerUID: "target", TopologyUID: "target", ResourceUID: "new", Generation: 2}, Fingerprint: "sha256:" + strings.Repeat("a", 64)}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	for _, change := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "changed"}[change], func(t *testing.T) {
			calls := 0
			inspected := false
			authorize := func(context.Context, controlplane.OwnershipTransferPlan) (controlplane.OwnershipPreparation, error) {
				calls++
				epoch := uint64(1)
				if change && calls == 2 {
					epoch++
				}
				return controlplane.OwnershipPreparation{Plan: p, SourceEpoch: epoch}, nil
			}
			proof, err := PrepareOwnership(context.Background(), p, "host", "boot", key, authorize, func(context.Context, controlplane.OwnershipTransferPlan) error {
				require.Equal(t, 1, calls)
				inspected = true
				return nil
			})
			require.True(t, inspected)
			require.Equal(t, 2, calls)
			if change {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NoError(t, controlplane.VerifyOwnershipPreparation(pub, proof))
			}
		})
	}
}
