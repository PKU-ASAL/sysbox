package firecracker

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestFencedAdoptionReanchorsOnlyStoppedPreviousBoot(t *testing.T) {
	for _, mode := range []string{"valid", "denied", "wrong-owner", "same-boot", "bad-receipt", "attaching", "lost-archive", "advanced-process"} {
		t.Run(mode, func(t *testing.T) {
			s, h, m, _ := manifestFixture(t, false)
			hs := h.Provider.(*HandleState)
			dir := filepath.Join(s.rootfsDir, "owner", "resource", "1")
			require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0700))
			require.NoError(t, os.Rename(hs.VMDir, dir))
			hs.VMDir = dir
			hs.ManifestPath = filepath.Join(dir, "manifest.json")
			hs.ConfigPath = filepath.Join(dir, "vm_config.json")
			hs.Socket = ownedUnixSocketPath(dir, h.ID, "firecracker.sock", "fc")
			hs.TopologyUID = "owner"
			m["topology_uid"] = "owner"
			oldBoot := "11111111-1111-4111-8111-111111111111"
			boot, err := currentBootID()
			require.NoError(t, err)
			m["process"] = map[string]any{"pid": 123, "start_time": "123", "boot_id": oldBoot, "executable": "/firecracker", "argv": []string{"/firecracker"}}
			m["future_extension"] = "keep-original"
			if mode == "same-boot" {
				m["process"].(map[string]any)["boot_id"] = boot
			}
			if mode == "attaching" {
				m["phase"] = "attaching"
			}
			writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
			before, err := os.ReadFile(rawManifestPath(h))
			require.NoError(t, err)
			a := controlplane.AdoptionRequest{ID: "adopt", Topology: "lab", TopologyUID: "owner", Address: "sysbox_node.vm", Provider: "firecracker", AgentID: "host", ExternalID: h.ID, ResourceUID: "resource", Generation: 1, Evidence: substrate.AdoptionEvidence{Provider: "firecracker", ExternalID: h.ID, Fingerprint: "before"}}
			if mode == "wrong-owner" {
				a.ResourceUID = "foreign"
			}
			digest, err := a.Digest()
			require.NoError(t, err)
			pub, key, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			g := controlplane.ExecutionGrant{AgentID: "host", SessionID: "session", CommandID: "command", ActionID: a.ID, Attempt: 1, Epoch: 1, Operation: "adoption_apply", Digest: digest, TargetBootID: oldBoot, ExpiresAt: time.Now().Add(time.Minute), State: controlplane.GrantActive, Scope: controlplane.ExecutionScope{TopologyUID: a.TopologyUID, ResourceUID: a.ResourceUID, Generation: 1}}
			req := controlplane.HostFenceRequest{Version: 1, ID: "fence", Topology: "lab", RunID: a.ID, LabID: "isolated", ControllerKey: pub, JournalSHA256: digest, Grant: g}
			rd, err := req.Digest()
			require.NoError(t, err)
			receipt, err := controlplane.SignHostFenceReceipt(key, req, controlplane.HostFenceReceipt{Version: 1, RequestSHA256: rd, LabID: req.LabID, OldBootID: oldBoot, NewBootID: boot, ControllerBootID: "33333333-3333-4333-8333-333333333333", ControllerBootUnchanged: true, QEMUPID: 42, QEMUStart: "99", QEMUExited: true, DiskDevice: 1, DiskInode: 2, ProviderScope: "local-only", OperationResult: "unknown", ObservedAt: time.Now().UTC()})
			require.NoError(t, err)
			if mode == "bad-receipt" {
				receipt.Signature = nil
			}
			p := controlplane.AdoptionDispositionProof{Request: req, Fence: receipt, Adoption: a}
			authorize := func(context.Context) error {
				if mode == "denied" {
					return errors.New("fence released")
				}
				return nil
			}
			recovered, err := s.RecoverFencedAdoption(context.Background(), p, authorize)
			if mode != "valid" && mode != "lost-archive" && mode != "advanced-process" {
				require.Error(t, err)
				after, e := os.ReadFile(rawManifestPath(h))
				require.NoError(t, e)
				require.Equal(t, before, after)
				return
			}
			require.NoError(t, err)
			obs, err := s.ObserveNode(context.Background(), recovered)
			require.NoError(t, err)
			require.True(t, obs.Exists)
			require.False(t, obs.Running)
			archives, err := filepath.Glob(filepath.Join(s.rootfsDir, ".locks", "adoption-fenced-*.json"))
			require.NoError(t, err)
			require.Len(t, archives, 1)
			var archive struct{ Manifest []byte }
			raw, err := os.ReadFile(archives[0])
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &archive))
			require.Equal(t, before, archive.Manifest)
			_, err = s.RecoverFencedAdoption(context.Background(), p, authorize)
			require.NoError(t, err)
			if mode == "lost-archive" {
				require.NoError(t, os.Remove(archives[0]))
				_, err = s.RecoverFencedAdoption(context.Background(), p, authorize)
				require.Error(t, err)
			}
			if mode == "advanced-process" {
				raw, err := os.ReadFile(rawManifestPath(h))
				require.NoError(t, err)
				var advanced vmManifest
				require.NoError(t, json.Unmarshal(raw, &advanced))
				advanced.Process = processIdentity{PID: 123, BootID: boot, StartTime: "new"}
				advanced.Phase = "running"
				require.NoError(t, saveManifest(&advanced))
				before, err := os.ReadFile(rawManifestPath(h))
				require.NoError(t, err)
				_, err = s.RecoverFencedAdoption(context.Background(), p, authorize)
				require.Error(t, err)
				after, err := os.ReadFile(rawManifestPath(h))
				require.NoError(t, err)
				require.Equal(t, before, after)
			}
			require.DirExists(t, h.Provider.(*HandleState).VMDir)
		})
	}
}

func TestColdStartRecreatesOwnedTemporarySocketDirectoryAfterHostReboot(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	s, h, _, _ := manifestFixture(t, false)
	hs := h.Provider.(*HandleState)
	dir := filepath.Dir(hs.Socket)
	require.NotEqual(t, hs.VMDir, dir, "exercise temporary short-socket directory")
	require.NoError(t, os.Remove(dir))
	s.firecrackerBin = "/bin/false"
	require.Error(t, s.StartNode(context.Background(), h), "stub exits after directory preparation")
	require.DirExists(t, dir, "cold start must recreate its lost socket parent before launching")
}
