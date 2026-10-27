package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeProcessDisappearingDuringStatRead(t *testing.T) {
	boot, err := currentBootID()
	require.NoError(t, err)
	anchor := processIdentity{PID: 123, StartTime: "1", BootID: boot, Executable: "/test", Argv: []string{"/test"}}
	for _, errno := range []error{syscall.ENOENT, syscall.ESRCH, syscall.EACCES} {
		live, _, err := probeProcessWithStat(anchor, func(string) ([]byte, error) {
			return nil, &os.PathError{Op: "read", Path: "/proc/123/stat", Err: errno}
		})
		require.False(t, live)
		if errno == syscall.EACCES {
			require.ErrorIs(t, err, errno)
		} else {
			require.NoError(t, err)
		}
	}
}

func TestFirecrackerManagedGCRejectsManifestOwnershipDrift(t *testing.T) {
	s, h, raw, _ := manifestFixture(t, false)
	raw["owner_uid"] = "other-owner"
	writeFixtureManifest(t, filepath.Dir(rawManifestPath(h)), raw)
	// The manifest path is derived from the canonical runtime root and VM ID;
	// a mismatched owner must fail before DestroyNode can touch the process.
	err := s.DeleteManagedExternal(context.Background(), h.ID, "owner", "resource", 1)
	require.Error(t, err)
}

func rawManifestPath(h substrate.NodeHandle) string {
	hs, _ := h.Provider.(*HandleState)
	return hs.ManifestPath
}

// This subprocess holds only a Unix socket. It never opens KVM or network devices.
func TestManifestProcessHelper(t *testing.T) {
	if os.Getenv("SYSBOX_MANIFEST_PROCESS_HELPER") != "1" {
		return
	}
	socket := os.Getenv("SYSBOX_MANIFEST_SOCKET")
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		err = syscall.Bind(fd, &syscall.SockaddrUnix{Name: socket})
	}
	if err == nil {
		err = syscall.Listen(fd, 4)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer syscall.Close(fd)
	for {
		conn, _, err := syscall.Accept(fd)
		if err != nil {
			os.Exit(0)
		}
		syscall.Close(conn)
	}
}

func manifestFixture(t *testing.T, live bool) (*Substrate, substrate.NodeHandle, map[string]any, *exec.Cmd) {
	t.Helper()
	root := t.TempDir()
	id := fmt.Sprintf("manifest-%x", sha256.Sum256([]byte(root)))[:22]
	dir := filepath.Join(root, id)
	require.NoError(t, os.Mkdir(dir, 0700))
	cfg := filepath.Join(dir, "vm_config.json")
	require.NoError(t, os.WriteFile(cfg, []byte(`{}`), 0600))
	socket := ownedUnixSocketPath(dir, id, "firecracker.sock", "fc")
	require.NoError(t, ensureOwnedSocketDirectory(socket))
	hs := &HandleState{VMDir: dir, ConfigPath: cfg, Socket: socket}
	// Use the current schema except in explicit old-format rejection tests.
	raw, _ := json.Marshal(map[string]any{"version": manifestVersion, "manifest_path": filepath.Join(dir, "manifest.json"), "owner_uid": "owner", "resource_uid": "resource", "generation": 1})
	require.NoError(t, json.Unmarshal(raw, hs))
	manifest := map[string]any{"version": manifestVersion, "vm_id": id, "runtime_root": root, "owner_uid": "owner", "resource_uid": "resource", "generation": 1, "action_id": "action", "config_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(`{}`))), "phase": "prepared", "handle": hs}
	var cmd *exec.Cmd
	if live {
		executable, err := os.Executable()
		require.NoError(t, err)
		cmd = exec.Command(executable, "-test.run=^TestManifestProcessHelper$", "--", "--config-file", cfg, "--api-sock", socket)
		cmd.Env = append(os.Environ(), "SYSBOX_MANIFEST_PROCESS_HELPER=1", "SYSBOX_MANIFEST_SOCKET="+socket)
		cmd.Stderr = os.Stderr
		require.NoError(t, cmd.Start())
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = os.Remove(socket) })
		require.Eventually(t, func() bool { _, err := os.Stat(socket); return err == nil }, time.Second*3, time.Millisecond)
		boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		require.NoError(t, err)
		manifest["process"] = map[string]any{"pid": cmd.Process.Pid, "start_time": processStartTime(cmd.Process.Pid), "boot_id": strings.TrimSpace(string(boot)), "executable": executable, "argv": cmd.Args}
		manifest["phase"] = "running"
	}
	writeFixtureManifest(t, dir, manifest)
	s := &Substrate{rootfsDir: root}
	h := substrate.NodeHandle{ID: id, Generation: 1, Provider: hs}
	t.Cleanup(func() { vmMu.Lock(); delete(vmStore, hs.VMDir); vmMu.Unlock() })
	return s, h, manifest, cmd
}

func writeFixtureManifest(t *testing.T, dir string, m map[string]any) {
	t.Helper()
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600))
}

func TestManifestColdPauseResumeAndStop(t *testing.T) {
	s, h, _, cmd := manifestFixture(t, true)
	require.NoError(t, s.Pause(context.Background(), h))
	require.Eventually(t, func() bool {
		raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
		return strings.Contains(string(raw), ") T ")
	}, time.Second, time.Millisecond)
	obs, err := s.ObserveNode(context.Background(), h)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusPaused, obs.Status)
	require.NoError(t, s.Resume(context.Background(), h))
	require.NoError(t, s.StopNode(context.Background(), h))
	require.Eventually(t, func() bool { return processIsZombie(cmd.Process.Pid) || !processAlive(cmd.Process.Pid) }, time.Second, time.Millisecond)
}

func TestManifestOwnershipConflictCannotSignalOrDelete(t *testing.T) {
	s, h, m, cmd := manifestFixture(t, true)
	m["owner_uid"] = "foreign"
	writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
	obs, err := s.ObserveNode(context.Background(), h)
	require.Error(t, err)
	require.False(t, obs.Running)
	require.Error(t, s.StopNode(context.Background(), h))
	require.Error(t, s.DestroyNode(context.Background(), h))
	require.NoError(t, cmd.Process.Signal(syscall.Signal(0)))
	require.DirExists(t, h.Provider.(*HandleState).VMDir)
}

func TestManifestMissingRecordIsUnknownAndCreateRetainsResidue(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "orphan")
	require.NoError(t, os.Mkdir(dir, 0700))
	sentinel := filepath.Join(dir, "rootfs.ext4")
	require.NoError(t, os.WriteFile(sentinel, []byte("retain"), 0600))
	s := &Substrate{rootfsDir: root}
	h := substrate.NodeHandle{ID: "orphan", Provider: &HandleState{VMDir: dir}}
	obs, err := s.ObserveNode(context.Background(), h)
	require.Error(t, err)
	require.Equal(t, substrate.NodeStatusUnknown, obs.Status)
	_, err = s.CreateNode(context.Background(), substrate.NodeSpec{Name: "orphan"})
	require.ErrorContains(t, err, "identity")
	raw, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	require.Equal(t, "retain", string(raw))
	require.Error(t, s.DestroyNode(context.Background(), h))
	require.FileExists(t, sentinel)
}

func TestManifestConfigMutationAndBootIdentityFailClosed(t *testing.T) {
	for _, kind := range []string{"config", "boot", "argv", "start"} {
		t.Run(kind, func(t *testing.T) {
			s, h, m, cmd := manifestFixture(t, true)
			switch kind {
			case "config":
				require.NoError(t, os.WriteFile(h.Provider.(*HandleState).ConfigPath, []byte(`{"changed":true}`), 0600))
			case "boot":
				m["process"].(map[string]any)["boot_id"] = "different"
			case "argv":
				m["process"].(map[string]any)["argv"] = []string{"wrong"}
			case "start":
				m["process"].(map[string]any)["start_time"] = "0"
			}
			writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
			require.Error(t, s.DestroyNode(context.Background(), h))
			require.NoError(t, cmd.Process.Signal(syscall.Signal(0)))
		})
	}
}

func TestManifestCachedRunningCannotProveLiveness(t *testing.T) {
	s, h, m, _ := manifestFixture(t, false)
	m["phase"] = "running"
	writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
	obs, err := s.ObserveNode(context.Background(), h)
	require.NoError(t, err)
	require.False(t, obs.Running)
	require.False(t, obs.Healthy)
}

func TestManifestStartingWithoutAnchorRetainsAmbiguity(t *testing.T) {
	s, h, m, _ := manifestFixture(t, false)
	m["phase"] = "starting"
	m["process"] = map[string]any{"executable": "/definitely/missing", "argv": []string{"/definitely/missing"}}
	writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
	obs, err := s.ObserveNode(context.Background(), h)
	require.ErrorContains(t, err, "unresolved launch")
	require.Equal(t, substrate.NodeStatusUnknown, obs.Status)
	require.Error(t, s.StartNode(context.Background(), h))
	require.Error(t, s.DestroyNode(context.Background(), h))
	require.DirExists(t, h.Provider.(*HandleState).VMDir)
}

func TestManifestSymlinkAndForeignPathRejectBeforeEffects(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0600))
	hs := h.Provider.(*HandleState)
	require.NoError(t, os.Remove(hs.ConfigPath))
	require.NoError(t, os.Symlink(sentinel, hs.ConfigPath))
	require.ErrorContains(t, s.DestroyNode(context.Background(), h), "symlink")
	require.FileExists(t, sentinel)
	hs.VMDir = outside
	require.Error(t, s.DestroyNode(context.Background(), h))
	require.DirExists(t, outside)
}

func TestManifestColdClientHelper(t *testing.T) {
	if os.Getenv("SYSBOX_MANIFEST_CLIENT") == "" {
		return
	}
	var h substrate.NodeHandle
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("SYSBOX_MANIFEST_CLIENT_HANDLE")), &h))
	var hs HandleState
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("SYSBOX_MANIFEST_CLIENT_STATE")), &hs))
	h.Provider = &hs
	s := &Substrate{rootfsDir: os.Getenv("SYSBOX_MANIFEST_CLIENT_ROOT")}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	switch os.Getenv("SYSBOX_MANIFEST_CLIENT") {
	case "blocked":
		_, err := s.ObserveNode(ctx, h)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case "recover":
		obs, err := s.ObserveNode(context.Background(), h)
		require.NoError(t, err)
		require.True(t, obs.Running)
		_, err = s.AdoptNode(context.Background(), h)
		require.NoError(t, err)
		require.Nil(t, lookupVM(h.Provider.(*HandleState).VMDir).snapshot().cmd)
		require.NoError(t, s.Pause(context.Background(), h))
		require.NoError(t, s.Resume(context.Background(), h))
		require.NoError(t, s.StopNode(context.Background(), h))
	case "destroy":
		require.NoError(t, s.DestroyNode(context.Background(), h))
	}
}

func coldManifestClient(t *testing.T, s *Substrate, h substrate.NodeHandle, action string) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	raw, err := json.Marshal(h)
	require.NoError(t, err)
	state, err := json.Marshal(h.Provider)
	require.NoError(t, err)
	cmd := exec.Command(exe, "-test.run=^TestManifestColdClientHelper$")
	cmd.Env = append(os.Environ(), "SYSBOX_MANIFEST_CLIENT="+action, "SYSBOX_MANIFEST_CLIENT_ROOT="+s.rootfsDir, "SYSBOX_MANIFEST_CLIENT_HANDLE="+string(raw), "SYSBOX_MANIFEST_CLIENT_STATE="+string(state))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestManifestRecoversAcrossIndependentClientProcesses(t *testing.T) {
	s, h, _, _ := manifestFixture(t, true)
	coldManifestClient(t, s, h, "recover")
	coldManifestClient(t, s, h, "destroy")
	require.NoDirExists(t, h.Provider.(*HandleState).VMDir)
}

func TestManifestOperationLockFencesIndependentProcesses(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	unlock, err := s.lockHostOperation(context.Background(), h)
	require.NoError(t, err)
	coldManifestClient(t, s, h, "blocked")
	unlock()
	require.NoError(t, s.DestroyNode(context.Background(), h))
	require.DirExists(t, filepath.Join(s.rootfsDir, ".locks"))
	entries, err := os.ReadDir(filepath.Join(s.rootfsDir, ".locks"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestManifestCodecRejectsLegacyProviderState(t *testing.T) {
	_, err := (&Substrate{}).UnmarshalProviderState(json.RawMessage(`{"vm_dir":"/tmp/legacy"}`))
	require.ErrorContains(t, err, "unsupported")
}

// Earlier fixtures wrote separate PID and metadata files. The new disk format
// deliberately requires the complete manifest, including process proof.
func seedOwnedManifest(t *testing.T, s *Substrate, id string, hs *HandleState, cmd *exec.Cmd) *vmManifest {
	t.Helper()
	root, err := s.canonicalRoot()
	require.NoError(t, err)
	if hs.VMDir == "" {
		hs.VMDir = filepath.Join(root, id)
	}
	require.NoError(t, os.MkdirAll(hs.VMDir, 0700))
	if hs.Socket != "" {
		if st, e := os.Lstat(hs.Socket); e == nil && st.Mode()&os.ModeSocket == 0 {
			require.NoError(t, os.Remove(hs.Socket))
		}
	}
	hs.Socket = ownedUnixSocketPath(hs.VMDir, id, "firecracker.sock", "fc")
	hs.ConfigPath = filepath.Join(hs.VMDir, "vm_config.json")
	require.NoError(t, os.WriteFile(hs.ConfigPath, []byte(`{}`), 0600))
	m := newManifest(substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{OwnerUID: "fixture", ResourceUID: id, TopologyUID: hs.TopologyUID, Generation: 1, ActionID: "fixture"}), root, id, hs.VMDir, hs, "sha256:fixture")
	if cmd != nil {
		p, e := captureProcessIdentity(cmd.Process.Pid)
		if e != nil {
			boot, err := currentBootID()
			require.NoError(t, err)
			exe, err := filepath.EvalSymlinks(cmd.Path)
			require.NoError(t, err)
			p = processIdentity{PID: cmd.Process.Pid, StartTime: processStartTime(cmd.Process.Pid), BootID: boot, Executable: exe, Argv: cmd.Args}
		}
		m.Process = p
		m.Phase = "running"
		hs.PID = p.PID
		hs.PIDStart = p.StartTime
		m.Handle = *hs
	}
	require.NoError(t, refreshConfigDigest(m))
	require.NoError(t, saveManifest(m))
	return m
}

func TestManifestRecoversLaunchWindowFromExactSocketOwner(t *testing.T) {
	s, h, m, cmd := manifestFixture(t, true)
	m["phase"] = "starting"
	m["process"].(map[string]any)["pid"] = 0
	m["process"].(map[string]any)["start_time"] = ""
	writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
	obs, err := s.ObserveNode(context.Background(), h)
	require.NoError(t, err)
	require.True(t, obs.Running)
	require.Equal(t, cmd.Process.Pid, obs.PID)
	recovered, err := s.loadManifest(context.Background(), h)
	require.NoError(t, err)
	require.Equal(t, cmd.Process.Pid, recovered.Process.PID)
}

func TestManifestLaunchRecoveryRejectsSocketFromDifferentProcess(t *testing.T) {
	s, h, m, cmd := manifestFixture(t, true)
	// Another exact command line exists, but it does not own this socket.
	imposter := exec.Command("sleep", "30")
	require.NoError(t, imposter.Start())
	t.Cleanup(func() { _ = imposter.Process.Kill(); _ = imposter.Wait() })
	var p processIdentity
	// exec completion can precede a readable /proc cmdline snapshot. The
	// provider must keep treating that interval as unknown, not as ownership.
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		var err error
		p, err = captureProcessIdentity(imposter.Process.Pid)
		require.NoError(c, err)
	}, time.Second, time.Millisecond)
	p.PID = 0
	p.StartTime = ""
	m["phase"] = "starting"
	m["process"] = p
	writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
	obs, err := s.ObserveNode(context.Background(), h)
	require.Error(t, err)
	require.False(t, obs.Running)
	require.NoError(t, cmd.Process.Signal(syscall.Signal(0)))
	require.NoError(t, imposter.Process.Signal(syscall.Signal(0)))
}

func TestManifestMissingDirectoryDoesNotHideOwnedProcess(t *testing.T) {
	s, h, _, _ := manifestFixture(t, true)
	hs := h.Provider.(*HandleState)
	raw, err := os.ReadFile(hs.ManifestPath)
	require.NoError(t, err)
	var m vmManifest
	require.NoError(t, json.Unmarshal(raw, &m))
	hs.PID = m.Process.PID
	hs.PIDStart = m.Process.StartTime
	require.NoError(t, os.RemoveAll(hs.VMDir))
	obs, err := s.ObserveNode(context.Background(), h)
	require.Error(t, err)
	require.Equal(t, substrate.NodeStatusUnknown, obs.Status)
	require.Error(t, s.DestroyNode(context.Background(), h))
}

func TestManifestMissingDirectoryWithExitedUnreapedProcess(t *testing.T) {
	s, h, _, cmd := manifestFixture(t, true)
	hs := h.Provider.(*HandleState)
	hs.PID, hs.PIDStart = cmd.Process.Pid, processStartTime(cmd.Process.Pid)
	require.NoError(t, cmd.Process.Kill())
	// Do not Wait: a cold controller cannot reap a different process's child.
	require.Eventually(t, func() bool { return processIsZombie(cmd.Process.Pid) }, 3*time.Second, time.Millisecond)
	require.True(t, processAlive(cmd.Process.Pid), "signal zero also succeeds for zombies")
	require.NoError(t, os.RemoveAll(hs.VMDir))
	require.NoError(t, os.Remove(hs.Socket))
	_, err := s.loadManifest(context.Background(), h)
	require.ErrorIs(t, err, errGenerationAbsent)
}

func TestMissingManifestProcessRequiresMatchingStartTime(t *testing.T) {
	for _, start := range []string{"", "not-the-process-start"} {
		s, h, _, cmd := manifestFixture(t, true)
		hs := h.Provider.(*HandleState)
		hs.PID, hs.PIDStart = cmd.Process.Pid, start
		require.NoError(t, cmd.Process.Kill())
		require.Eventually(t, func() bool { return processIsZombie(cmd.Process.Pid) }, 3*time.Second, time.Millisecond)
		require.NoError(t, os.RemoveAll(hs.VMDir))
		_, err := s.loadManifest(context.Background(), h)
		require.ErrorContains(t, err, "process identity conflict")
	}
}

func TestManifestManagedDiscoveryRetainsGenerationIdentity(t *testing.T) {
	root := t.TempDir()
	s := &Substrate{rootfsDir: root}
	identity := substrate.OperationIdentity{OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 9, ActionID: "create-action"}
	_, err := s.DiscoverManagedNode(context.Background(), identity, "node")
	require.True(t, driver.IsCategory(err, driver.ErrorNotFound))
	dir := filepath.Join(root, identity.TopologyUID, identity.ResourceUID, "9")
	require.NoError(t, os.MkdirAll(dir, 0700))
	hs := &HandleState{VMDir: dir, Socket: ownedUnixSocketPath(dir, "node", "firecracker.sock", "fc"), ConfigPath: filepath.Join(dir, "vm_config.json")}
	require.NoError(t, os.WriteFile(hs.ConfigPath, []byte(`{}`), 0600))
	m := newManifest(substrate.WithOperationIdentity(context.Background(), identity), root, "node", dir, hs, "sha256:fixture")
	require.NoError(t, refreshConfigDigest(m))
	require.NoError(t, saveManifest(m))
	found, err := s.DiscoverManagedNode(context.Background(), identity, "node")
	require.NoError(t, err)
	require.Equal(t, int64(9), found.Generation)
	identity.ActionID = "observe-attempt"
	_, err = s.DiscoverManagedNode(context.Background(), identity, "node")
	require.NoError(t, err)
	identity.OwnerUID = "wrong"
	_, err = s.DiscoverManagedNode(context.Background(), identity, "node")
	require.Error(t, err)
	require.False(t, driver.IsCategory(err, driver.ErrorNotFound))
}

func TestManifestCreatePersistsCanonicalManagedGeneration(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "image.ext4")
	require.NoError(t, os.WriteFile(source, []byte("harmless image fixture"), 0600))
	s := &Substrate{rootfsDir: root, kernelPath: "/fixture/kernel"}
	op := substrate.OperationIdentity{ActionID: "create", OwnerUID: "owner", ResourceUID: "resource", TopologyUID: "topology", Generation: 3}
	ctx := substrate.WithOperationIdentity(context.Background(), op)
	h, err := s.CreateNode(ctx, substrate.NodeSpec{Name: "node", Image: substrate.ArtifactHandle{ID: source}})
	require.NoError(t, err)
	hs := h.Provider.(*HandleState)
	require.Equal(t, filepath.Join(root, "topology", "resource", "3"), hs.VMDir)
	m, err := s.loadManifest(ctx, h)
	require.NoError(t, err)
	require.NotEmpty(t, m.ConfigSHA256)
	require.NotEmpty(t, m.ImageDigest)
	require.Equal(t, "create", m.ActionID)
	require.NoFileExists(t, filepath.Join(hs.VMDir, "firecracker.pid"))
	require.NoFileExists(t, filepath.Join(hs.VMDir, "sysbox.json"))
	found, err := s.DiscoverManagedNode(ctx, op, "node")
	require.NoError(t, err)
	require.Equal(t, h.ID, found.ID)
	require.NoError(t, s.DestroyNode(ctx, h))
}

func TestManifestHandlePathConflictCannotSignal(t *testing.T) {
	s, h, _, _ := manifestFixture(t, true)
	h.Provider.(*HandleState).Socket = "/tmp/foreign.sock"
	require.Error(t, s.StopNode(context.Background(), h))
}

func TestManifestInvalidCreateHasNoGenerationEffects(t *testing.T) {
	root := t.TempDir()
	s := &Substrate{rootfsDir: root}
	_, err := s.CreateNode(substrate.WithOperationIdentity(context.Background(), substrate.OperationIdentity{OwnerUID: "owner", ResourceUID: "invalid", TopologyUID: "topology", Generation: 1, ActionID: "create"}), substrate.NodeSpec{Name: "invalid", Memory: "broken"})
	require.ErrorContains(t, err, "memory")
	require.NoDirExists(t, filepath.Join(root, "invalid"))
}

func TestManifestRegistryScopesSameIDToRuntimeRoot(t *testing.T) {
	s1 := &Substrate{rootfsDir: t.TempDir()}
	s2 := &Substrate{rootfsDir: t.TempDir()}
	hs1, hs2 := &HandleState{}, &HandleState{}
	seedOwnedManifest(t, s1, "same-id", hs1, nil)
	seedOwnedManifest(t, s2, "same-id", hs2, nil)
	h1, h2 := firecrackerNodeHandle("same-id", hs1), firecrackerNodeHandle("same-id", hs2)
	_, _, release, err := s1.lockedManifest(context.Background(), h1)
	require.NoError(t, err)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, _, release2, err := s2.lockedManifest(ctx, h2)
	require.NoError(t, err)
	release2()
}

func TestManifestExitedAdoptedProcessDoesNotBlockNewStart(t *testing.T) {
	s, h, _, cmd := manifestFixture(t, true)
	_, err := s.AdoptNode(context.Background(), h)
	require.NoError(t, err)
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	s.firecrackerBin = "/definitely/missing-firecracker"
	err = s.StartNode(context.Background(), h)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "already in progress")
}

func TestManifestUnfinishedAttachmentCannotStart(t *testing.T) {
	s, h, m, _ := manifestFixture(t, false)
	m["phase"] = "attaching"
	writeFixtureManifest(t, h.Provider.(*HandleState).VMDir, m)
	err := s.StartNode(context.Background(), h)
	require.ErrorContains(t, err, "attachment")
	require.FileExists(t, h.Provider.(*HandleState).ManifestPath)
}

func TestManifestPausedStopConfirmsProcessExit(t *testing.T) {
	s, h, _, cmd := manifestFixture(t, true)
	require.NoError(t, s.Pause(context.Background(), h))
	require.NoError(t, s.StopNode(context.Background(), h))
	require.Eventually(t, func() bool { return processIsZombie(cmd.Process.Pid) || !processAlive(cmd.Process.Pid) }, time.Second, time.Millisecond)
}

func TestManifestPreparedConnectionCoordinatesSurviveColdAdoption(t *testing.T) {
	s, h, _, _ := manifestFixture(t, true)
	h.Net.PrimaryIP = "10.0.0.2"
	require.NoError(t, s.PrepareHandle(context.Background(), &h, &Config{SSHUser: "ubuntu", SSHPort: 2222, SSHPass: "do-not-persist"}, nilStateReader{}))
	adopted, err := s.AdoptNode(context.Background(), h)
	require.NoError(t, err)
	hs := adopted.Provider.(*HandleState)
	require.Equal(t, "10.0.0.2", hs.SSHIP)
	require.Equal(t, "ubuntu", hs.SSHUser)
	require.Equal(t, "2222", hs.SSHPort)
	raw, err := os.ReadFile(hs.ManifestPath)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "do-not-persist")
}

func TestManifestMissingConfigIsUnknownAndRetained(t *testing.T) {
	s, h, _, _ := manifestFixture(t, false)
	hs := h.Provider.(*HandleState)
	require.NoError(t, os.Remove(hs.ConfigPath))
	obs, err := s.ObserveNode(context.Background(), h)
	require.Error(t, err)
	require.Equal(t, substrate.NodeStatusUnknown, obs.Status)
	require.Error(t, s.DestroyNode(context.Background(), h))
	require.DirExists(t, hs.VMDir)
}

func TestProbeProcessExitBetweenIdentityReads(t *testing.T) {
	boot, err := currentBootID()
	require.NoError(t, err)
	anchor := processIdentity{PID: 123, StartTime: "77", BootID: boot, Executable: "/test", Argv: []string{"/test"}}
	snapshot := func(status string, start string) []byte {
		fields := make([]string, 20)
		for i := range fields {
			fields[i] = "0"
		}
		fields[0] = status
		fields[19] = start
		return []byte("123 (test) " + strings.Join(fields, " "))
	}
	for _, mode := range []string{"zombie", "gone", "reused", "denied", "still-live", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			read := func(string) ([]byte, error) {
				calls++
				if calls == 1 {
					return snapshot("R", "77"), nil
				}
				switch mode {
				case "zombie":
					return snapshot("Z", "77"), nil
				case "gone":
					return nil, syscall.ESRCH
				case "reused":
					return snapshot("Z", "78"), nil
				case "still-live":
					return snapshot("R", "77"), nil
				case "malformed":
					return []byte("broken"), nil
				default:
					return nil, syscall.EACCES
				}
			}
			live, _, err := probeProcessWithReaders(anchor, read, func(int) (processIdentity, error) { return processIdentity{}, fmt.Errorf("empty cmdline during exit") })
			require.False(t, live)
			if mode == "zombie" || mode == "gone" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
