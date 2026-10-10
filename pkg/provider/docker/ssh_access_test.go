package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/stretchr/testify/require"
)

func TestDockerSSHAccessFencesBeforeGuestExecution(t *testing.T) {
	for _, operation := range []string{"discover", "delete", "observe", "inspect"} {
		t.Run(operation, func(t *testing.T) {
			inspections, effects := 0, 0
			s := managedDockerClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "GET" {
					inspections++
					actual := testOperation()
					actual.Generation++
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "node", "Config": map[string]any{"Labels": operationLabels(actual)}})
					return
				}
				effects++
				w.WriteHeader(500)
			})
			conn, err := s.Connection(substrate.NodeHandle{ID: "node", Generation: 3}, nil)
			require.NoError(t, err)
			access, ok := conn.(substrate.ConnectionSSHAccess)
			require.True(t, ok, "production Docker connection must expose managed SSH SPI")
			ctx := substrate.WithOperationIdentity(context.Background(), testOperation())
			query := substrate.SSHAccessQuery{NodeID: "node", User: "ubuntu", AccessID: "admin", TopologyUID: "topology", ResourceUID: "resource", Generation: 3}
			h := substrate.SSHAccessHandle{NodeID: "node", User: "ubuntu", AccessID: "admin", TopologyUID: "topology", ResourceUID: "resource", Generation: 3, ID: "action", Digest: "digest", Revision: 1, BootID: "boot", Epoch: 1}
			switch operation {
			case "discover":
				_, err = access.DiscoverSSHAccess(ctx, query)
			case "observe":
				_, err = access.ObserveSSHAccess(ctx, h)
			case "inspect":
				_, err = access.InspectSSHAccess(ctx, h)
			case "delete":
				req := substrate.SSHAccessMutation{ID: "delete", Delete: true, ExpectedRevision: 1, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
				digest, digestErr := req.Digest()
				require.NoError(t, digestErr)
				grant := controlplane.SignedExecutionGrant{BootID: "boot", Grant: controlplane.ExecutionGrant{TargetBootID: "boot", Scope: controlplane.ExecutionScope{TopologyUID: "topology", ResourceUID: "resource", Generation: 3}, ActionID: req.ID, Operation: "guest_ssh_access_delete", Digest: digest, Epoch: 1}}
				_, err = access.MutateSSHAccess(transport.WithTaskGrant(ctx, req.ID, grant), req)
			}
			require.Error(t, err)
			require.Equal(t, 1, inspections)
			require.Zero(t, effects, "stale generation cannot start Docker exec")
		})
	}
}
