package libvirt

import (
	"context"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestObserveNodeReadsActualManagedMetadata(t *testing.T) {
	for _, kind := range []string{"valid", "wrong-owner", "wrong-generation", "absent"} {
		t.Run(kind, func(t *testing.T) {
			id := managedOperation()
			actual := id
			if kind == "wrong-owner" {
				actual.OwnerUID = "foreign"
			}
			if kind == "wrong-generation" {
				actual.Generation++
			}
			hs := &HandleState{Identity: &id, DomainName: "node", DomainUUID: "11111111-2222-4333-8444-555555555555"}
			raw, err := GenerateDomainXML(DomainSpec{Name: hs.DomainName, UUID: hs.DomainUUID, Identity: &actual, ManagedState: hs})
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "domain.xml")
			require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
			t.Setenv("OBSERVE_XML", path)
			t.Setenv("OBSERVE_UUID", hs.DomainUUID)
			list := "printf '%s\\n' \"$OBSERVE_UUID\""
			if kind == "absent" {
				list = "exit 0"
			}
			virshFixture(t, "#!/bin/sh\ncase \"$1\" in\nlist) "+list+";;\ndumpxml) cat \"$OBSERVE_XML\";;\ndomstate) [ \"$2\" = \"$OBSERVE_UUID\" ] || exit 5; printf 'paused\\n';;\ndomuuid) printf '%s\\n' \"$OBSERVE_UUID\";;\nesac\n")
			result, err := New().ObserveNode(substrate.WithOperationIdentity(context.Background(), id), managedNodeHandle(hs))
			switch kind {
			case "valid":
				require.NoError(t, err)
				require.Equal(t, id.OwnerUID, result.OwnerUID)
				require.Equal(t, id.ResourceUID, result.ResourceUID)
				require.Equal(t, id.Generation, result.Generation)
				require.Equal(t, substrate.NodeStatusPaused, result.Status)
			case "absent":
				require.NoError(t, err)
				require.False(t, result.Exists)
			default:
				require.Error(t, err)
				require.False(t, result.Healthy)
			}
		})
	}
}
