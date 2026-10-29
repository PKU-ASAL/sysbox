//go:build e2e

package firecracker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestJailerRealIsolationRestartAndColdCleanupE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	id := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "jailer-node", Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "jailer-test", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	defer func() {
		if t.Failed() {
			raw, _ := os.ReadFile(filepath.Join(h.Provider.(*HandleState).VMDir, "firecracker.log"))
			t.Logf("VMM startup log: %s", raw)
		}
	}()
	require.NoError(t, s.StartNode(ctx, h))
	m, err := s.loadManifest(ctx, h)
	require.NoError(t, err)
	require.NotNil(t, m.Jail)
	defer func() {
		if t.Failed() {
			for _, name := range []string{"memory.events", "memory.current", "pids.events"} {
				raw, _ := os.ReadFile(filepath.Join(jailCgroup(m.Jail), name))
				t.Logf("%s: %s", name, raw)
			}
		}
	}()
	require.NoError(t, verifyJailedProcess(m, m.Process))
	require.NotZero(t, m.Jail.UID)
	require.NotEqual(t, m.Handle.VMDir, m.Jail.Root)
	require.NoFileExists(t, filepath.Join(m.Jail.Root, "manifest.json"))
	require.NoError(t, s.Pause(ctx, h))
	require.NoError(t, s.Resume(ctx, h))
	require.NoError(t, s.StopNode(ctx, h))
	require.Eventually(t, func() bool { alive, _, err := probeProcess(m.Process); return err == nil && !alive }, 5*time.Second, 20*time.Millisecond, "StopNode must actually stop namespace PID1")
	require.NoError(t, s.StartNode(ctx, h))
	restarted, err := s.loadManifest(ctx, h)
	require.NoError(t, err)
	require.NotEqual(t, m.Process.PID, restarted.Process.PID)
	require.NoError(t, verifyJailedProcess(restarted, restarted.Process))
	cold := realFirecracker(t, kernel, bin, s.rootfsDir)
	require.NoError(t, cold.DestroyNode(ctx, h))
	require.NoDirExists(t, m.Jail.Root)
	require.NoDirExists(t, jailCgroup(m.Jail))
	_, err = os.Stat(h.Provider.(*HandleState).ManifestPath)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, cold.DestroyNode(ctx, h))
	t.Log("real KVM: unique nonroot UID, jail root, PID/mount/IPC/UTS namespaces, seccomp, cgroup, pause/resume, stop/start and cold destroy verified")
}

func TestJailerRealNetworkAndLaunchRecoveryE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	name := "sbj-" + uuid.NewString()[:8]
	netID := substrate.OperationIdentity{ActionID: "network", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: name, Generation: 1}
	netCtx := substrate.WithOperationIdentity(ctx, netID)
	netSpec := driver.IsolatedNetworkSpec{Name: name, Bridge: "br0", CIDR: "10.44.0.1/24"}
	require.NoError(t, (network.Driver{}).CreateIsolated(netCtx, netSpec))
	t.Cleanup(func() {
		require.NoError(t, (network.Driver{}).DeleteIsolated(substrate.WithOperationIdentity(context.Background(), netID), netSpec))
	})
	id := netID
	id.ResourceUID = "node"
	id.ActionID = "create"
	ctx = substrate.WithOperationIdentity(ctx, id)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "jailer-net", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel}})
	require.NoError(t, err)
	cleanupRealVM(t, s, h)
	raw, err := json.Marshal(map[string]string{"netns": name, "bridge": "br0"})
	require.NoError(t, err)
	req := driver.AttachmentRequest{Name: "nic", NetworkIdentity: netID, IPPrefixes: []string{"10.44.0.2/24"}, NetworkState: raw}
	attached, err := s.Attach(ctx, h, req)
	require.NoError(t, err)
	var tap attachmentState
	require.NoError(t, json.Unmarshal(attached.State, &tap))
	require.NotNil(t, tap.Link)
	alias, err := network.AttachmentAlias(id, req.Name)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		require.NoError(t, network.DeleteOwnedAttachment(cleanup, *tap.Link, alias))
	})

	require.NoError(t, s.StartNode(ctx, h))
	m, err := s.loadManifest(ctx, h)
	require.NoError(t, err)
	require.NoError(t, verifyJailedProcess(m, m.Process))
	conn, err := s.Connection(h, nil)
	require.NoError(t, err)
	result, err := conn.Exec(ctx, substrate.ExecRequest{Program: "ping", Args: []string{"-c", "1", "-W", "2", "10.44.0.1"}, Shell: substrate.ShellNone}, nil, nil)
	require.NoError(t, err)
	require.Zero(t, result.ExitCode, result.Stderr)
	oldPID := m.Process.PID
	m.Phase = "starting"
	m.Process = processIdentity{BootID: m.Jail.BootID}
	require.NoError(t, saveManifest(m))
	cold := realFirecracker(t, kernel, bin, s.rootfsDir)
	require.NoError(t, cold.StartNode(ctx, h))
	m, err = cold.loadManifest(ctx, h)
	require.NoError(t, err)
	require.Equal(t, oldPID, m.Process.PID, "unknown launch must recover the original VMM")
	require.NoError(t, cold.DestroyNode(ctx, h))
	owner, err := exec.CommandContext(ctx, ipBin, "netns", "exec", name, ipBin, "tuntap", "show", "dev", tap.Tap).CombinedOutput()
	require.NoError(t, err, string(owner))
	require.NotContains(t, string(owner), "user "+strconv.FormatUint(uint64(m.Jail.UID), 10))
	require.Contains(t, string(owner), "user 0", "retained TAP must lose the released jail UID")
	t.Log("real TAP networking under the jailed UID and launch-intent recovery without duplicate VMM verified")
}

func TestJailerUIDCollisionRetainsOriginalReservationE2E(t *testing.T) {
	bin, kernel, _ := realFirecrackerPaths(t)
	s := realFirecracker(t, kernel, bin, t.TempDir())
	ctx := context.Background()
	newRecord := func(id string) *vmManifest {
		dir := filepath.Join(s.rootfsDir, id)
		require.NoError(t, os.MkdirAll(dir, 0700))
		m := &vmManifest{VMID: id, Phase: "prepared", Handle: HandleState{VMDir: dir, ManifestPath: filepath.Join(dir, "manifest.json")}}
		j, err := s.newJailIdentity(m)
		require.NoError(t, err)
		m.Jail = &j
		m.Handle.JailRoot = j.Root
		require.NoError(t, saveManifest(m))
		return m
	}
	first := newRecord("uid-" + strings.ReplaceAll(uuid.NewString(), "-", ""))
	second := newRecord("uid-" + strings.ReplaceAll(uuid.NewString(), "-", ""))
	second.Jail.UID, second.Jail.GID = first.Jail.UID, first.Jail.GID
	require.NoError(t, reserveJailUID(ctx, first, *first.Jail))
	defer func() { require.NoError(t, removeJail(first)) }()
	require.ErrorContains(t, reserveJailUID(ctx, second, *second.Jail), "collision")
	require.NoError(t, removeJail(second), "failed unreserved generation must remain cleanable")
	require.NoError(t, reserveJailUID(ctx, first, *first.Jail), "original reservation must remain intact")
}

func TestJailerRealCleanupAfterPreparationCrashE2E(t *testing.T) {
	bin, kernel, rootfs := realFirecrackerPaths(t)
	for _, object := range []string{"cgroup", "root"} {
		t.Run(object, func(t *testing.T) {
			s := realFirecracker(t, kernel, bin, t.TempDir())
			ctx := substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", TopologyUID: "topology", ResourceUID: "node", Generation: 1})
			h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "crash", Image: substrate.ArtifactHandle{ID: rootfs}, ProviderConfig: &Config{Kernel: kernel}})
			require.NoError(t, err)
			cleanupRealVM(t, s, h)
			m, err := s.loadManifest(ctx, h)
			require.NoError(t, err)
			j, err := s.newJailIdentity(m)
			require.NoError(t, err)
			m.Jail = &j
			require.NoError(t, saveManifest(m))
			require.NoError(t, reserveJailUID(ctx, m, j))
			if object == "root" {
				require.NoError(t, os.MkdirAll(j.Root, 0700))
			} else {
				require.NoError(t, os.Mkdir(jailCgroup(&j), 0755))
			}
			require.NoError(t, s.DestroyNode(ctx, h), "cleanup must not require starting the VM")
			require.NoDirExists(t, j.Root)
			require.NoDirExists(t, jailCgroup(&j))
		})
	}
}
