package firecracker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestFencedResidueCleanupRequiresFreshGrantAndOldBootAnchor(t *testing.T) {
	for _, mode := range []string{"valid", "old-grant", "wrong-owner", "new-boot", "bad-proof", "later-boot"} {
		t.Run(mode, func(t *testing.T) {
			s, h, m, _ := manifestFixture(t, false)
			oldBoot := "11111111-1111-4111-8111-111111111111"
			boot, err := currentBootID()
			require.NoError(t, err)
			m["process"] = map[string]any{"pid": 123, "start_time": "123", "boot_id": oldBoot, "executable": "/firecracker", "argv": []string{"/firecracker"}}
			m["future_extension"] = "retain-exact-evidence"
			writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
			c := controlplane.GCCandidate{Kind: "firecracker", ExternalID: h.ID, OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "old-inventory"}
			digest, _ := c.Digest()
			issuer, issuerKey, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			host, hostKey, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			controller, controllerKey, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			old := controlplane.ExecutionGrant{Scope: controlplane.ExecutionScope{TopologyUID: "owner", ResourceUID: "resource", Generation: 1}, AgentID: "host", SessionID: "session", CommandID: "old-gc", ActionID: "old-gc", Attempt: 1, Epoch: 1, Operation: "gc_delete", Digest: digest, TargetBootID: oldBoot, ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive}
			req := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: old.CommandID, LabID: "isolated", ControllerKey: controller, JournalSHA256: strings.Repeat("a", 64), Grant: old}
			rd, err := req.Digest()
			require.NoError(t, err)
			fence, err := controlplane.SignHostFenceReceipt(controllerKey, req, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: rd, LabID: req.LabID, OldBootID: oldBoot, NewBootID: func() string {
				if mode == "later-boot" {
					return "22222222-2222-4222-8222-222222222222"
				}
				return boot
			}(), ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "99", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
			require.NoError(t, err)
			proof, err := controlplane.SignGCDisposition(hostKey, controlplane.GCDispositionProof{Request: req, Fence: fence, Candidate: c, HostBootID: fence.NewBootID, Presence: "present", ObservedAt: time.Now().UTC()})
			require.NoError(t, err)
			c.InventoryDigest = "new-inventory"
			next := old
			next.Epoch = 3
			next.CommandID = "new-gc"
			next.ActionID = "new-gc"
			next.TargetBootID = boot
			next.Digest, _ = c.Digest()
			switch mode {
			case "old-grant":
				next = old
			case "wrong-owner":
				c.OwnerUID = "foreign"
			case "new-boot":
				m["process"].(map[string]any)["boot_id"] = boot
				writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
			case "bad-proof":
				proof.Signature = nil
			}
			grant, err := controlplane.SignExecutionGrant(issuerKey, next, next.TargetBootID, time.Now())
			require.NoError(t, err)
			err = s.DeleteFencedGCResidue(context.Background(), c, proof, grant, issuer, host)
			if mode != "valid" && mode != "later-boot" {
				require.Error(t, err)
				require.DirExists(t, h.Provider.(*HandleState).VMDir)
				return
			}
			require.NoError(t, err)
			archives, err := filepath.Glob(filepath.Join(s.rootfsDir, ".locks", "fenced-cleanup-*.json"))
			require.NoError(t, err)
			require.Len(t, archives, 1)
			raw, err := os.ReadFile(archives[0])
			require.NoError(t, err)
			var archive struct {
				Manifest []byte `json:"manifest"`
			}
			require.NoError(t, json.Unmarshal(raw, &archive))
			require.Contains(t, string(archive.Manifest), "retain-exact-evidence")
			require.NoDirExists(t, h.Provider.(*HandleState).VMDir)
			_, err = os.Stat(h.Provider.(*HandleState).Socket)
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestPreparedGCManifestNeedsNormalDeletionNotFenceProof(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	c := controlplane.GCCandidate{Kind: "firecracker", ExternalID: h.ID, OwnerUID: "owner", TopologyUID: "owner", ResourceUID: "resource", Generation: 1, InventoryDigest: "inventory"}
	boot, err := s.FencedGCManifestBoot(context.Background(), c)
	require.NoError(t, err)
	require.Empty(t, boot)
	require.NoError(t, s.DeleteManagedExternal(context.Background(), h.ID, "owner", "resource", 1))
}
