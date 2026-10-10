package transport

import (
	"context"
	"testing"

	"github.com/oslab/sysbox/pkg/guesttask"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestSSHDiscoveryRejectsUnboundOrInconsistentResponses(t *testing.T) {
	query := substrate.SSHAccessQuery{NodeID: "node", User: "ubuntu", AccessID: "admin", TopologyUID: "owner", ResourceUID: "resource", Generation: 1}
	identity := executionIdentity{nodeID: "node", generation: 1}
	request := substrate.SSHAccessMutation{ID: "delete", ExpectedRevision: 1, Delete: true, Config: substrate.SSHAccessConfig{User: "ubuntu", AccessID: "admin"}}
	digest, err := request.Digest()
	require.NoError(t, err)
	for _, scenario := range []string{"tombstone", "missing", "query", "owner", "revision", "digest", "config", "presence", "boot", "version", "guest-error"} {
		t.Run(scenario, func(t *testing.T) {
			caller := sshAccessCaller(func(call guesttask.Call) guesttask.Response {
				require.Equal(t, "ssh-discover", call.Operation)
				require.Empty(t, call.SSHQuery.NodeID)
				config := request.Config
				found := substrate.SSHAccessDiscovery{Query: *call.SSHQuery, BootID: "current-boot", Status: "absent", Config: &config,
					Current: &substrate.SSHAccessResult{Handle: substrate.SSHAccessHandle{ID: "delete", User: "ubuntu", AccessID: "admin", Digest: digest,
						Revision: 2, Epoch: 2, BootID: "historical-boot", TopologyUID: "owner", ResourceUID: "resource", Generation: 1}}}
				response := guesttask.Response{Version: guesttask.Version, SSHDiscovery: &found}
				switch scenario {
				case "missing":
					found.Current, found.Config = nil, nil
				case "query":
					found.Query.AccessID = "other"
				case "owner":
					found.Current.Handle.TopologyUID = "other"
				case "revision":
					found.Current.Handle.Revision = 0
				case "digest":
					found.Current.Handle.Digest = "wrong"
				case "config":
					found.Config = nil
				case "presence":
					found.Status = "present"
				case "boot":
					found.BootID = ""
				case "version":
					response.Version++
				case "guest-error":
					response.Error = "read failed"
				}
				return response
			})
			found, err := discoverSSHAccess(context.Background(), caller, identity, query)
			if scenario == "tombstone" || scenario == "missing" {
				require.NoError(t, err)
				require.Equal(t, "absent", found.Status)
				if scenario == "tombstone" {
					require.Equal(t, "node", found.Current.Handle.NodeID)
				}
			} else {
				require.Error(t, err)
				require.Equal(t, "unknown", found.Status)
				require.Nil(t, found.Current)
			}
		})
	}
	query.Generation++
	_, err = discoverSSHAccess(context.Background(), sshAccessCaller(func(guesttask.Call) guesttask.Response {
		t.Fatal("wrong generation dispatched")
		return guesttask.Response{}
	}), identity, query)
	require.Error(t, err)
}
