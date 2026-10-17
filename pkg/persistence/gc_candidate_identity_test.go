package persistence

import (
	"encoding/json"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

func TestGCReferencesProtectUIDAndProviderExternalIDIndependently(t *testing.T) {
	for _, test := range []struct {
		name, uid, id, provider string
		generation              int64
		blocked                 bool
	}{
		{"resource UID across generations", "target", "different", "libvirt", 7, true},
		{"provider external ID under another UID", "different", "external", "docker", 1, true},
		{"same external ID in another provider", "different", "external", "libvirt", 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := newAggregate()
			a.State.Resources = []state.Resource{{Address: address.Resource("sysbox_node", "node"), OwnerUID: a.State.TopologyUID, ResourceUID: test.uid, ExternalID: test.id, Driver: test.provider, Generation: test.generation}}
			inventory, err := gcReferences(a)
			require.NoError(t, err)
			raw, err := json.Marshal(a)
			require.NoError(t, err)
			command := controlplane.AgentCommand{Type: "gc_delete", TopologyUID: a.State.TopologyUID, ResourceUID: "target", Generation: 1, GCKind: "container", GCExternalID: "external", GCInventoryDigest: inventory.Digest}
			_ = raw
			err = validateGCReferences(a, command)
			if test.blocked {
				require.ErrorIs(t, err, ErrConflict)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
