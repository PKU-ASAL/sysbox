package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"golang.org/x/sys/unix"
)

const manifestVersion = 4

// Only this sentinel means the entire owned generation was proved absent.
// ENOENT for a config or another required file is incomplete evidence.
var errGenerationAbsent = errors.New("firecracker generation absent")

type processIdentity struct {
	PID        int      `json:"pid"`
	StartTime  string   `json:"start_time"`
	BootID     string   `json:"boot_id"`
	Executable string   `json:"executable"`
	Argv       []string `json:"argv"`
}

// vmManifest is the sole durable authority for provider identity and recovery.
// Phase and Ready describe past observations; neither is evidence of liveness.
type vmManifest struct {
	Jail                 *jailIdentity              `json:"jail,omitempty"`
	AdoptionFence        string                     `json:"adoption_fence,omitempty"`
	AdoptionFenceArchive string                     `json:"adoption_fence_archive,omitempty"`
	Transfer             *manifestOwnershipTransfer `json:"ownership_transfer,omitempty"`
	Version              int                        `json:"version"`
	VMID                 string                     `json:"vm_id"`
	RuntimeRoot          string                     `json:"runtime_root"`
	OwnerUID             string                     `json:"owner_uid"`
	ResourceUID          string                     `json:"resource_uid"`
	TopologyUID          string                     `json:"topology_uid,omitempty"`
	Generation           int64                      `json:"generation"`
	ActionID             string                     `json:"action_id"`
	ConfigSHA256         string                     `json:"config_sha256"`
	ImageDigest          string                     `json:"image_digest,omitempty"`
	Phase                string                     `json:"phase"`
	ObservedAt           time.Time                  `json:"observed_at"`
	Process              processIdentity            `json:"process"`
	Handle               HandleState                `json:"handle"`
}

func validIdentityPart(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, "/\\\x00")
}

func (s *Substrate) canonicalRoot() (string, error) {
	if s.rootfsDir == "" {
		return "", fmt.Errorf("firecracker runtime root missing")
	}
	root, err := filepath.Abs(s.rootfsDir)
	if err != nil {
		return "", err
	}
	if err := rejectSymlinks(root); err != nil {
		return "", err
	}
	return root, nil
}

func ensureOwnedSocketDirectory(socket string) error {
	dir := filepath.Dir(socket)
	if err := rejectSymlinks(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || !ok || stat.Uid != uint32(os.Geteuid()) || st.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("firecracker socket directory is not controlled by the current owner")
	}
	return nil
}

// Check each component, including an existing leaf. Missing suffixes are allowed.
func rejectSymlinks(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("firecracker noncanonical path %q", path)
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("firecracker refuses symlink path %q", current)
		}
	}
	return nil
}

func (s *Substrate) manifestDirectory(h substrate.NodeHandle) (string, error) {
	root, err := s.canonicalRoot()
	if err != nil {
		return "", err
	}
	if !validIdentityPart(h.ID) {
		return "", fmt.Errorf("firecracker invalid VM identity %q", h.ID)
	}
	dir := filepath.Join(root, h.ID)
	if hs, _ := h.Provider.(*HandleState); hs != nil && hs.VMDir != "" {
		dir = hs.VMDir
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", fmt.Errorf("firecracker refuses unowned VM directory %q", dir)
	}
	if err := rejectSymlinks(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// The lock lives outside all generation directories and is never unlinked.
// Deleting a locked file would let another process lock a different inode.
func (s *Substrate) lockHostOperation(ctx context.Context, h substrate.NodeHandle) (func(), error) {
	dir, err := s.manifestDirectory(h)
	if err != nil {
		return nil, err
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return nil, err
	}
	lockDir := filepath.Join(root, ".locks")
	if err := rejectSymlinks(lockDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(dir))
	path := filepath.Join(lockDir, fmt.Sprintf("%x.lock", sum))
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			unix.Close(fd)
			return nil, err
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = unix.Close(fd) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			unix.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
			unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func readRegularFile(path string) ([]byte, error) {
	if err := rejectSymlinks(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("firecracker expected regular file %q", path)
	}
	return os.ReadFile(fmt.Sprintf("/proc/self/fd/%d", fd))
}

func atomicWriteFile(path string, raw []byte, mode os.FileMode) error {
	if err := rejectSymlinks(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func saveManifest(m *vmManifest) error {
	m.ObservedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(m.Handle.ManifestPath, raw, 0600)
}

func refreshConfigDigest(m *vmManifest) error {
	raw, err := readRegularFile(m.Handle.ConfigPath)
	if err != nil {
		return err
	}
	m.ConfigSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	return nil
}

func (s *Substrate) loadManifest(ctx context.Context, h substrate.NodeHandle) (*vmManifest, error) {
	dir, err := s.manifestDirectory(h)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "manifest.json")
	raw, err := readRegularFile(path)
	if os.IsNotExist(err) {
		if _, e := os.Lstat(dir); os.IsNotExist(e) {
			if id, ok := substrate.OperationIdentityFromContext(ctx); ok {
				if err := network.VMNamespaceNameAbsent(id); err != nil {
					return nil, err
				}
			}
			if hs, ok := h.Provider.(*HandleState); ok && hs.TopologyUID != "" {
				id := substrate.OperationIdentity{ActionID: "absence", OwnerUID: hs.OwnerUID, TopologyUID: hs.TopologyUID, ResourceUID: hs.ResourceUID, Generation: hs.Generation}
				if err := network.VMNamespaceNameAbsent(id); err != nil {
					return nil, err
				}
			}

			if err := s.jailResidueAbsent(h.ID); err != nil {
				return nil, err
			}
			if supplied, ok := h.Provider.(*HandleState); ok && supplied.JailRoot != "" {
				if _, e := os.Lstat(supplied.JailRoot); !os.IsNotExist(e) {
					return nil, fmt.Errorf("jail exists or cannot be observed after metadata removal")
				}
			}

			if supplied, _ := h.Provider.(*HandleState); supplied != nil && supplied.PID > 0 {
				if err := confirmMissingManifestProcessExited(supplied); err != nil {
					return nil, err
				}
			}
			if residue, probeErr := hasLaunchResidue(filepath.Join(dir, "vm_config.json")); probeErr != nil {
				return nil, probeErr
			} else if residue {
				return nil, fmt.Errorf("firecracker manifest directory missing while launch residue exists")
			}
			socket := ownedUnixSocketPath(dir, h.ID, "firecracker.sock", "fc")
			if _, e := os.Lstat(socket); e == nil {
				return nil, fmt.Errorf("firecracker orphan socket exists without manifest")
			} else if !os.IsNotExist(e) {
				return nil, e
			}
			if supplied, ok := h.Provider.(*HandleState); ok {
				if supplied.FabricPending != "" {
					return nil, fmt.Errorf("VM namespace creation outcome unknown")
				}
				if supplied.Fabric != nil {
					for _, a := range supplied.Attachments {
						if a.Circuit == nil {
							return nil, fmt.Errorf("VM circuit proof missing")
						}
						if err := network.VMCircuitAbsent(ctx, *supplied.Fabric, *a.Circuit); err != nil {
							return nil, err
						}
					}
					if err := network.VMNamespaceAbsent(ctx, *supplied.Fabric); err != nil {
						return nil, err
					}
				}
			}
			return nil, errGenerationAbsent
		}
		return nil, fmt.Errorf("firecracker manifest missing: legacy or orphan VM directory %q requires explicit rebuild", dir)
	}
	if err != nil {
		return nil, err
	}
	var m vmManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("firecracker invalid manifest: %w", err)
	}
	root, _ := s.canonicalRoot()
	if m.Version != manifestVersion && !(m.Version == 3 && m.Handle.Fabric == nil && m.Handle.FabricPending == "") {
		return nil, fmt.Errorf("firecracker manifest version %d unsupported; rebuild required", m.Version)
	}
	if m.VMID != h.ID || m.RuntimeRoot != root || !validIdentityPart(m.OwnerUID) || !validIdentityPart(m.ResourceUID) || m.Generation <= 0 || m.ActionID == "" {
		return nil, fmt.Errorf("firecracker manifest identity conflict")
	}
	expected := filepath.Join(root, m.VMID)
	if m.TopologyUID != "" {
		if !validIdentityPart(m.TopologyUID) {
			return nil, fmt.Errorf("invalid manifest topology identity")
		}
		expected = filepath.Join(root, m.TopologyUID, m.ResourceUID, strconv.FormatInt(m.Generation, 10))
	}
	if m.Transfer != nil {
		expected, err = transferManifestDirectory(root, &m)
		if err != nil {
			return nil, err
		}
	}
	if dir != expected {
		return nil, fmt.Errorf("firecracker manifest directory identity conflict")
	}
	hs := &m.Handle
	if err := validateFabricHandle(*hs, manifestNetworkIdentity(&m)); err != nil {
		return nil, err
	}
	expectedSocket := ownedUnixSocketPath(dir, h.ID, "firecracker.sock", "fc")
	expectedVsock := ownedUnixSocketPath(dir, h.ID, "vsock.sock", "vs")
	if hs.JailRoot != "" {
		if !filepath.IsAbs(hs.JailRoot) || filepath.Clean(hs.JailRoot) != hs.JailRoot || filepath.Base(hs.JailRoot) != "root" || filepath.Base(filepath.Dir(hs.JailRoot)) != m.VMID {
			return nil, fmt.Errorf("invalid jail root binding")
		}
		if err := rejectSymlinks(hs.JailRoot); err != nil {
			return nil, err
		}
		expectedSocket = filepath.Join(hs.JailRoot, "run/firecracker.sock")
		expectedVsock = filepath.Join(hs.JailRoot, "run/vsock.sock")
	}
	if m.Jail != nil {
		if m.Jail.Root != hs.JailRoot {
			return nil, fmt.Errorf("manifest jail root conflict")
		}
		if _, err := jailerArguments(&m, *m.Jail); err != nil {
			return nil, err
		}
	}
	if !supportedNetworkHandle(*hs) || hs.ManifestPath != path || hs.VMDir != dir || hs.ConfigPath != filepath.Join(dir, "vm_config.json") || hs.Socket != expectedSocket {
		return nil, fmt.Errorf("firecracker manifest path identity conflict")
	}
	if hs.OwnerUID != m.OwnerUID || hs.ResourceUID != m.ResourceUID || hs.Generation != m.Generation || hs.TopologyUID != m.TopologyUID {
		return nil, fmt.Errorf("firecracker manifest handle identity conflict")
	}
	if hs.VsockUDS != "" && hs.VsockUDS != expectedVsock {
		return nil, fmt.Errorf("firecracker manifest vsock path conflict")
	}
	for _, p := range []string{hs.ConfigPath, hs.Socket, hs.VsockUDS, filepath.Join(dir, "rootfs.ext4"), filepath.Join(dir, "config.ext4")} {
		if p != "" {
			if err := rejectSymlinks(p); err != nil {
				return nil, err
			}
		}
	}
	for _, p := range []string{hs.Socket, hs.VsockUDS} {
		if p != "" {
			st, e := os.Lstat(p)
			if e == nil {
				stat, ok := st.Sys().(*syscall.Stat_t)
				owner := uint32(os.Geteuid())
				if m.Jail != nil {
					owner = m.Jail.UID
				}
				if !ok || stat.Uid != owner {
					return nil, fmt.Errorf("firecracker socket owner conflict")
				}
			}
			if e == nil && st.Mode()&os.ModeSocket == 0 {
				return nil, fmt.Errorf("firecracker expected socket %q", p)
			}
			if e != nil && !os.IsNotExist(e) {
				return nil, e
			}
		}
	}
	if h.Generation != 0 && h.Generation != m.Generation {
		return nil, fmt.Errorf("firecracker manifest generation conflict")
	}
	if supplied, _ := h.Provider.(*HandleState); supplied != nil {
		if !supportedNetworkHandle(*supplied) || supplied.ManifestPath != path || supplied.OwnerUID != m.OwnerUID || supplied.ResourceUID != m.ResourceUID || supplied.Generation != m.Generation || supplied.TopologyUID != m.TopologyUID || supplied.JailRoot != hs.JailRoot || supplied.Socket != hs.Socket || supplied.ConfigPath != hs.ConfigPath || supplied.VsockUDS != hs.VsockUDS {
			return nil, fmt.Errorf("firecracker supplied handle identity conflict")
		}
	}
	if op, ok := substrate.OperationIdentityFromContext(ctx); ok {
		if op.OwnerUID != m.OwnerUID || op.ResourceUID != m.ResourceUID || op.Generation != m.Generation || (op.TopologyUID != "" && op.TopologyUID != m.TopologyUID) {
			return nil, fmt.Errorf("firecracker operation identity conflict")
		}
	}
	raw, err = readRegularFile(hs.ConfigPath)
	if err != nil {
		return nil, err
	}
	if m.ConfigSHA256 != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		return nil, fmt.Errorf("firecracker config digest conflict")
	}
	return &m, nil
}

func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("firecracker image is not a regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", digest.Sum(nil)), nil
}

func hasLaunchResidue(configPath string) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == "--config-file" && argv[i+1] == configPath {
				return true, nil
			}
		}
	}
	return false, nil
}

func processOwnsSocket(pid int, path string) (bool, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/unix", pid))
	if err != nil {
		return false, err
	}
	inodes := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 8 && strings.Join(fields[7:], " ") == path {
			inodes["socket:["+fields[6]+"]"] = true
		}
	}
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, entry.Name()))
		if err == nil && inodes[target] {
			return true, nil
		}
	}
	return false, nil
}

func newManifest(ctx context.Context, root, id, dir string, hs *HandleState, imageDigest string) *vmManifest {
	op, _ := substrate.OperationIdentityFromContext(ctx)
	hs.Version = manifestVersion
	hs.ManifestPath = filepath.Join(dir, "manifest.json")
	hs.OwnerUID = op.OwnerUID
	hs.ResourceUID = op.ResourceUID
	hs.TopologyUID = op.TopologyUID
	hs.Generation = op.Generation
	return &vmManifest{Version: manifestVersion, VMID: id, RuntimeRoot: root, OwnerUID: op.OwnerUID, ResourceUID: op.ResourceUID, TopologyUID: op.TopologyUID, Generation: op.Generation, ActionID: op.ActionID, ImageDigest: imageDigest, Phase: "prepared", Handle: *hs}
}

func currentBootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(raw)), err
}

// Empty cmdline can occur during exit before procfs publishes Z/X. This is
// uncertainty, not absence; only a bounded post-signal waiter may recheck it.
var errProcessIdentityTransition = errors.New("firecracker process identity changed during probe")

func captureProcessIdentity(pid int) (processIdentity, error) {
	boot, err := currentBootID()
	if err != nil {
		return processIdentity{}, err
	}
	start := processStartTime(pid)
	if start == "" {
		return processIdentity{}, fmt.Errorf("firecracker process identity unavailable")
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return processIdentity{}, err
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return processIdentity{}, err
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if len(raw) == 0 {
		return processIdentity{}, errProcessIdentityTransition
	}
	return processIdentity{PID: pid, StartTime: start, BootID: boot, Executable: exe, Argv: argv}, nil
}

// A mismatch is uncertainty, not absence. Never delete artifacts or signal a
// possibly recycled process in response to mismatched identity evidence.
func probeProcess(anchor processIdentity) (bool, bool, error) {
	return probeProcessWithStat(anchor, os.ReadFile)
}
func probeProcessWithStat(anchor processIdentity, readStat func(string) ([]byte, error)) (bool, bool, error) {
	return probeProcessWithReaders(anchor, readStat, captureProcessIdentity)
}
func probeProcessWithReaders(anchor processIdentity, readStat func(string) ([]byte, error), capture func(int) (processIdentity, error)) (bool, bool, error) {
	if anchor.PID <= 0 {
		return false, false, nil
	}
	if anchor.StartTime == "" || anchor.BootID == "" || anchor.Executable == "" || len(anchor.Argv) == 0 {
		return false, false, fmt.Errorf("firecracker incomplete process identity")
	}
	boot, err := currentBootID()
	if err != nil {
		return false, false, err
	}
	if boot != anchor.BootID {
		return false, false, fmt.Errorf("firecracker host boot identity conflict")
	}
	raw, err := readStat(fmt.Sprintf("/proc/%d/stat", anchor.PID))
	// procfs may open successfully and then return ESRCH from read when the
	// owned process is reaped concurrently. Both errors prove it disappeared;
	// permission and malformed-identity errors remain uncertainty.
	if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 || len(raw) <= end+2 {
		return false, false, fmt.Errorf("firecracker invalid process stat")
	}
	// Compare identity from the same stat snapshot. A second /proc read can
	// race the reaper and turn a disappeared owned process into a false PID
	// reuse conflict. Later executable/argv checks still verify live identity.
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return false, false, fmt.Errorf("firecracker invalid process stat")
	}
	if fields[19] != anchor.StartTime {
		return false, false, fmt.Errorf("firecracker process start identity conflict")
	}
	state := raw[end+2]
	if state == 'Z' || state == 'X' {
		return false, false, nil
	}
	actual, err := capture(anchor.PID)
	if err != nil {
		// Exit/reaping can race exe/cmdline. Use one final stat snapshot:
		// separate start-time and zombie reads can themselves race the reaper.
		final, statErr := readStat(fmt.Sprintf("/proc/%d/stat", anchor.PID))
		if os.IsNotExist(statErr) || errors.Is(statErr, syscall.ESRCH) {
			return false, false, nil
		}
		if statErr != nil {
			return false, false, statErr
		}
		end := strings.LastIndexByte(string(final), ')')
		if end < 0 || len(final) <= end+2 {
			return false, false, fmt.Errorf("firecracker invalid final process stat")
		}
		fields := strings.Fields(string(final[end+1:]))
		if len(fields) < 20 {
			return false, false, fmt.Errorf("firecracker invalid final process stat")
		}
		if fields[19] != anchor.StartTime {
			return false, false, fmt.Errorf("firecracker final process start identity conflict")
		}
		if fields[0] == "Z" || fields[0] == "X" {
			return false, false, nil
		}
		return false, false, err
	}
	if !reflect.DeepEqual(actual, anchor) {
		return false, false, fmt.Errorf("firecracker executable or argv identity conflict")
	}
	return true, state == 'T' || state == 't', nil
}

func signalManifestProcess(m *vmManifest, signal syscall.Signal) error {
	live, _, err := probeProcess(m.Process)
	if err != nil || !live {
		return err
	}
	fd, err := unix.PidfdOpen(m.Process.PID, 0)
	if err != nil {
		return fmt.Errorf("firecracker safe process signaling requires pidfd: %w", err)
	}
	defer unix.Close(fd)
	// Probe again after opening: an intervening PID reuse must not target its successor.
	live, _, err = probeProcess(m.Process)
	if err != nil || !live {
		return err
	}
	if err := unix.PidfdSendSignal(fd, unix.Signal(signal), nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

// A missing manifest supplies only PID/start time. Fail closed for living or
// mismatched processes, but a matching zombie has already stopped executing.
func confirmMissingManifestProcessExited(h *HandleState) error {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", h.PID))
	if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return fmt.Errorf("firecracker invalid process stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 || h.PIDStart == "" || fields[19] != h.PIDStart {
		return fmt.Errorf("firecracker missing manifest process identity conflict")
	}
	if fields[0] == "Z" || fields[0] == "X" {
		return nil
	}
	return fmt.Errorf("firecracker manifest directory missing while process anchor remains live")
}
