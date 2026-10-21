package hostsupervisor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/effectgate"
	"github.com/stretchr/testify/require"
)

func TestGCFencedDispositionArchivesUnknownJournalWithoutReplay(t *testing.T) {
	for _, fault := range []string{"intent", "missing", "corrupt"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			issuer, issuerKey, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			host, hostKey, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			controller, controllerKey, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			oldBoot, newBoot := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
			candidate := GCCandidate{Kind: "domain", ExternalID: "external", TopologyUID: "owner", OwnerUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "inventory"}
			digest, _ := candidate.Digest()
			grant := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "agent", SessionID: "session", CommandID: "gc", ActionID: "gc", Attempt: 1, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: oldBoot, State: controlplane.GrantActive, ExpiresAt: time.Now().UTC().Add(time.Minute)}
			signed, err := controlplane.SignExecutionGrant(issuerKey, grant, oldBoot, time.Now())
			require.NoError(t, err)
			c := &GCCoordinator{Root: t.TempDir(), IssuerKey: issuer, HostKey: hostKey, HostBootID: oldBoot, AgentID: "agent", Authorize: func(context.Context, controlplane.SignedExecutionGrant) error { return nil }, ValidateInventory: func(context.Context, GCCandidate) error { return nil }}
			_, err = c.Delete(ctx, candidate, signed, func(context.Context) (json.RawMessage, error) { return nil, fmt.Errorf("unknown") })
			require.ErrorIs(t, err, effectgate.ErrUncertain)
			path := filepath.Join(c.Root, digestHex([]byte("owner"))+".json")
			if fault == "missing" {
				require.NoError(t, os.Remove(path))
			}
			if fault == "corrupt" {
				require.NoError(t, os.WriteFile(path, []byte("broken"), 0600))
			}
			request := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: "gc", LabID: "isolated", ControllerKey: controller, JournalSHA256: strings.Repeat("a", 64), Grant: grant}
			requestDigest, err := request.Digest()
			require.NoError(t, err)
			fence, err := controlplane.SignHostFenceReceipt(controllerKey, request, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: requestDigest, LabID: request.LabID, OldBootID: oldBoot, NewBootID: newBoot, ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "123", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
			require.NoError(t, err)
			c.AuthorizeFence = func(context.Context, string) (controlplane.GCDispositionProof, error) {
				return controlplane.GCDispositionProof{Request: request, Fence: fence, Candidate: candidate}, nil
			}
			calls := 0
			c.InspectFencedCandidate = func(context.Context, GCCandidate) (string, error) { calls++; return "absent", nil }
			_, err = c.Disposition(ctx, "fence")
			require.Error(t, err, "old boot cannot attest new observation")
			require.Zero(t, calls)
			c.HostBootID = newBoot
			authorizations := 0
			c.AuthorizeFence = func(context.Context, string) (controlplane.GCDispositionProof, error) {
				authorizations++
				if authorizations == 2 {
					lock := flock.New(filepath.Join(c.Root, digestHex([]byte("owner"))+".lock"))
					held, err := lock.TryLock()
					require.NoError(t, err)
					lock.Close()
					require.False(t, held, "final canonical authorization must run under the same lock as journal replacement")
				}
				return controlplane.GCDispositionProof{Request: request, Fence: fence, Candidate: candidate}, nil
			}
			proof, err := c.Disposition(ctx, "fence")
			require.NoError(t, err)
			require.NoError(t, controlplane.VerifyGCDisposition(host, proof))
			require.Equal(t, 1, calls)
			cold := *c
			again, err := cold.Disposition(ctx, "fence")
			require.NoError(t, err)
			require.Equal(t, proof, again)
			require.Equal(t, 1, calls)
			_, err = c.Observe(ctx, candidate, signed)
			require.ErrorIs(t, err, effectgate.ErrUncertain)
			archives, err := filepath.Glob(filepath.Join(c.Root, "gc-fenced-*.evidence"))
			require.NoError(t, err)
			require.Len(t, archives, 1)
			// Repeating an already-admitted observation cannot erase a newer
			// journal when the archived preimage is incomplete/corrupt.
			archiveRaw, err := os.ReadFile(archives[0])
			require.NoError(t, err)
			var archived struct {
				Journal []byte `json:"journal"`
			}
			require.NoError(t, json.Unmarshal(archiveRaw, &archived))
			if fault == "missing" {
				require.NoError(t, os.Remove(path))
			} else {
				require.NoError(t, os.WriteFile(path, archived.Journal, 0600))
			}
			require.NoError(t, os.WriteFile(archives[0], []byte("truncated"), 0600))
			c.AuthorizeFence = func(context.Context, string) (controlplane.GCDispositionProof, error) { return proof, nil }
			_, err = c.Disposition(ctx, "fence")
			require.ErrorIs(t, err, effectgate.ErrUncertain)
			if fault == "missing" {
				require.NoFileExists(t, path)
			} else {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, archived.Journal, after)
			}
		})
	}
}
