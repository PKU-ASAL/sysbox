package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/transport"
	"github.com/oslab/sysbox/pkg/vsockrpc"
)

// vmProcess tracks a running Firecracker VM process.
type vmProcess struct {
	operation  chan struct{} // per-VM transition gate, initialized under mu
	mu         sync.Mutex
	cmd        *exec.Cmd
	pid        int
	startTime  string
	exitCode   *int
	exitError  string
	socket     string // API socket path
	vmID       string
	rootfs     string // per-VM rootfs copy
	cfgPath    string // VM config JSON path
	netnsName  string // network netns to run FC inside (empty = root netns)
	started    bool   // true after StartNode
	starting   bool   // true while a StartNode transition owns this VM
	destroying bool   // teardown has begun; starting again is forbidden
	destroyed  bool
	done       chan struct{} // closed by the sole child reaper
}

type vmProcessSnapshot struct {
	cmd        *exec.Cmd
	pid        int
	startTime  string
	socket     string
	cfgPath    string
	netnsName  string
	exitCode   *int
	exitError  string
	started    bool
	starting   bool
	destroying bool
	destroyed  bool
	done       <-chan struct{}
}

// lockOperation orders lifecycle calls without holding the registry or state
// mutex during I/O. A caller waiting on another transition can be canceled.
func (v *vmProcess) lockOperation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.mu.Lock()
	if v.operation == nil {
		v.operation = make(chan struct{}, 1)
	}
	gate := v.operation
	v.mu.Unlock()
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (v *vmProcess) unlockOperation() { <-v.operation }

func lookupVM(id string) *vmProcess {
	vmMu.Lock()
	defer vmMu.Unlock()
	return vmStore[id]
}

// A cold lifecycle call uses the same gate as later adoption/teardown calls.
// An empty entry is not a prepared VM and cannot be started.
func lookupOrReserveVM(id string) *vmProcess {
	vmMu.Lock()
	defer vmMu.Unlock()
	if vm := vmStore[id]; vm != nil {
		return vm
	}
	vm := &vmProcess{vmID: id}
	vmStore[id] = vm
	return vm
}

func (v *vmProcess) reserveStart() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.started || v.starting || v.destroying || v.destroyed {
		return false
	}
	v.starting = true
	return true
}

func (v *vmProcess) releaseStart() {
	v.mu.Lock()
	v.starting = false
	v.mu.Unlock()
}

func (v *vmProcess) snapshot() vmProcessSnapshot {
	v.mu.Lock()
	defer v.mu.Unlock()
	var exitCode *int
	if v.exitCode != nil {
		value := *v.exitCode
		exitCode = &value
	}
	return vmProcessSnapshot{
		cmd: v.cmd, pid: v.pid, startTime: v.startTime,
		socket: v.socket, cfgPath: v.cfgPath, netnsName: v.netnsName,
		exitCode: exitCode, exitError: v.exitError,
		started: v.started, starting: v.starting,
		destroying: v.destroying, destroyed: v.destroyed, done: v.done,
	}
}

// HandleState is the firecracker-substrate's typed NodeHandle.Provider payload.
// Persisted via MarshalProviderState so cold-destroy and drift refresh can
// rebuild the VM's working directory without rediscovery.
type HandleState struct {
	VMDir       string `json:"vm_dir,omitempty"`
	Socket      string `json:"socket,omitempty"`
	ConfigPath  string `json:"config_path,omitempty"`
	PIDFile     string `json:"pid_file,omitempty"`
	MetaPath    string `json:"meta_path,omitempty"`
	PID         int    `json:"pid,omitempty"`
	PIDStart    string `json:"pid_start,omitempty"`
	Ready       bool   `json:"ready,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	ExitError   string `json:"exit_error,omitempty"`
	CrashPolicy string `json:"crash_policy,omitempty"`

	VsockUDS  string `json:"vsock_uds,omitempty"`
	VsockCID  uint32 `json:"vsock_cid,omitempty"`
	VsockPort uint32 `json:"vsock_port,omitempty"`

	NICCount int    `json:"nic_count,omitempty"`
	TapName  string `json:"tap_name,omitempty"`

	NetnsName string `json:"netns_name,omitempty"`

	// SSHIP/SSHPort are populated by PrepareHandle as a fallback for VMs whose
	// rootfs lacks sysbox-init (no vsock channel). Substrate.Connection() reads
	// these to build the SSH connection.
	SSHIP   string `json:"ssh_ip,omitempty"`
	SSHPort string `json:"ssh_port,omitempty"`
	SSHUser string `json:"ssh_user,omitempty"`
	SSHPass string `json:"-"`
}

var (
	vmMu    sync.Mutex
	vmStore = map[string]*vmProcess{} // vm_id → vmProcess
)

func (s *Substrate) ResolveImage(_ context.Context, source substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	if source.Kind != substrate.ArtifactRootFS {
		return substrate.ArtifactHandle{}, fmt.Errorf("firecracker substrate requires artifact kind %q", substrate.ArtifactRootFS)
	}
	effective := source.ResolvedSource
	if effective == "" {
		effective = source.Source
	}
	resolved, err := artifact.New().ResolveIdentity(artifact.IdentitySpec{Kind: source.Kind, Source: effective, ExpectedDigest: source.ExpectedDigest, Architecture: source.Architecture, GuestFamily: source.GuestFamily, Metadata: source.Metadata})
	if err != nil {
		return substrate.ArtifactHandle{}, fmt.Errorf("firecracker: resolve rootfs: %w", err)
	}
	resolved.Identity.Source = source.Source
	return substrate.ArtifactHandle{Identity: resolved.Identity, ID: resolved.Path}, nil
}

// CreateNode prepares the VM config but does NOT start the VM yet.
// NICs are added via AttachNIC, then the VM is started via StartNode.
// This two-phase approach is needed because Firecracker requires all
// network interfaces to be declared in the boot config — no hot-plug.
func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	return s.createNodeWithID(ctx, spec, spec.Name)
}

func (s *Substrate) createNodeWithID(ctx context.Context, spec substrate.NodeSpec, vmID string) (substrate.NodeHandle, error) {
	vm := lookupOrReserveVM(vmID)
	if err := vm.lockOperation(ctx); err != nil {
		return substrate.NodeHandle{}, err
	}
	defer vm.unlockOperation()
	state := vm.snapshot()
	if state.started || state.destroying || state.destroyed {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker VM %s already running or being destroyed", vmID)
	}
	runDir := filepath.Join(s.rootfsDir, vmID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("create VM run dir: %w", err)
	}

	// Kill any leftover Firecracker process from a previous failed run.
	// A stale FC process holds the TAP fd open, preventing TAP reuse.
	if err := killOwnedStaleFirecracker(runDir, vmID, ownedUnixSocketPath(runDir, vmID, "firecracker.sock", "fc")); err != nil {
		return substrate.NodeHandle{}, err
	}

	pc, _ := spec.ProviderConfig.(*Config)
	if pc == nil {
		pc = &Config{}
	}

	// Copy the base rootfs so each VM has its own writable copy.
	imagePath := spec.Image.ID
	vmRootfs := filepath.Join(runDir, "rootfs.ext4")

	// Rootfs override: if provider config specifies rootfs, use it directly.
	if pc.Rootfs != "" {
		imagePath = pc.Rootfs
	}

	if err := copyFile(imagePath, vmRootfs); err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("copy rootfs: %w", err)
	}

	// Inject sysbox-init into the per-VM rootfs copy and build a per-VM
	// config drive. Both are best-effort: if sysbox-init was not built into
	// the host binary (placeholder embed), or mkfs.ext4/mount aren't
	// available, boot continues with the rootfs-provided init path.
	sysboxInitEnabled := true
	configDrivePath := filepath.Join(runDir, "config.ext4")
	if err := injectInitBinary(vmRootfs); err != nil {
		fmt.Printf("[firecracker] sysbox-init disabled for %s: %v\n", vmID, err)
		sysboxInitEnabled = false
	} else {
		nodeName := strings.TrimPrefix(spec.Name, "sysbox-")
		initCfg := vsockrpc.VMConfig{
			Hostname:  nodeName,
			Env:       spec.Env,
			ChainInit: pc.ChainInit,
		}
		if err := buildConfigDrive(configDrivePath, initCfg); err != nil {
			fmt.Printf("[firecracker] config drive build failed for %s: %v\n", vmID, err)
			sysboxInitEnabled = false
		}
	}

	// Determine kernel path: provider config override > substrate default.
	kernelPath := s.kernelPath
	if pc.Kernel != "" {
		kernelPath = pc.Kernel
	}

	// Generate initial VM config (no NICs yet — added by AttachNIC).
	vcpus := 2
	if spec.VCPUs > 0 {
		vcpus = spec.VCPUs
	}
	memMB := 512
	if spec.Memory != "" {
		parsed, err := parseMemoryMiB(spec.Memory)
		if err != nil {
			return substrate.NodeHandle{}, err
		}
		memMB = parsed
	}

	socketPath := ownedUnixSocketPath(runDir, vmID, "firecracker.sock", "fc")
	cfgPath := filepath.Join(runDir, "vm_config.json")
	pidPath := filepath.Join(runDir, "firecracker.pid")
	metaPath := filepath.Join(runDir, "sysbox.json")

	bootArgs := "console=ttyS0 reboot=k panic=1 pci=off init=/init root=/dev/vda rw"
	drives := []fcDrive{
		{DriveID: "rootfs", PathOnHost: vmRootfs, IsReadOnly: false, IsRootDevice: true},
	}
	if sysboxInitEnabled {
		bootArgs = "console=ttyS0 reboot=k panic=1 pci=off init=/sysbox-init root=/dev/vda rw"
		drives = append(drives, fcDrive{
			DriveID:      "config",
			PathOnHost:   configDrivePath,
			IsReadOnly:   true,
			IsRootDevice: false,
		})
	}

	cfg := fcConfig{
		BootSource: fcBootSource{
			KernelImagePath: kernelPath,
			BootArgs:        bootArgs,
		},
		MachineConfig: fcMachineConfig{
			VcpuCount: vcpus,
			MemSizeMB: memMB,
		},
		Drives: drives,
	}

	// Phase D: virtio-vsock device for in-VM RPC. The host talks to the guest
	// agent through this UDS (CONNECT <port>\n → OK <hostport>\n protocol).
	vsockUDS := ""
	var vsockCID uint32
	if sysboxInitEnabled {
		vsockUDS = ownedUnixSocketPath(runDir, vmID, "vsock.sock", "vs")
		_ = os.Remove(vsockUDS) // stale socket from a previous run
		vsockCID = s.allocCID()
		cfg.Vsock = &fcVsock{GuestCID: vsockCID, UDSPath: vsockUDS}
	}

	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("marshal VM config: %w", err)
	}
	if err := os.WriteFile(cfgPath, cfgData, 0644); err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("write VM config: %w", err)
	}

	vm.mu.Lock()
	vm.socket, vm.rootfs, vm.cfgPath = socketPath, vmRootfs, cfgPath
	vm.netnsName = ""
	vm.mu.Unlock()

	hs := &HandleState{
		VMDir:       runDir,
		Socket:      socketPath,
		ConfigPath:  cfgPath,
		PIDFile:     pidPath,
		MetaPath:    metaPath,
		CrashPolicy: "observe_only",
	}
	_ = writeVMMetadata(metaPath, hs)
	conn := substrate.ConnInfo{}
	if vsockUDS != "" {
		hs.VsockUDS = vsockUDS
		hs.VsockCID = vsockCID
		hs.VsockPort = uint32(8901)
		conn.Kind = substrate.ConnKindVsock
		conn.Endpoint = fmt.Sprintf("%s:%d", vsockUDS, hs.VsockPort)
	}
	return substrate.NodeHandle{
		ID:       vmID,
		Provider: hs,
		Conn:     conn,
	}, nil
}

// StartNode launches the Firecracker process with the completed config.
// This must be called after all AttachNIC calls are done.
func (s *Substrate) StartNode(ctx context.Context, h substrate.NodeHandle) error {
	vm := lookupVM(h.ID)
	if vm == nil {
		return fmt.Errorf("VM %s not found", h.ID)
	}
	if err := vm.lockOperation(ctx); err != nil {
		return err
	}
	defer vm.unlockOperation()
	vmState := vm.snapshot()
	if vmState.destroying || vmState.destroyed {
		return fmt.Errorf("firecracker VM %s is being destroyed", h.ID)
	}
	if vmState.cfgPath == "" {
		return fmt.Errorf("VM %s not prepared", h.ID)
	}
	if !vm.reserveStart() {
		if vm.snapshot().started {
			return nil // already running
		}
		return fmt.Errorf("firecracker VM %s start already in progress", h.ID)
	}
	defer vm.releaseStart()

	// Remove stale socket if present.
	if err := os.Remove(vmState.socket); err != nil && !os.IsNotExist(err) {
		slog.Debug("remove stale socket", "path", vmState.socket, "error", err)
	}

	// If a network netns is set, run Firecracker inside it so it can access
	// the TAP device and bridge that live in that netns.
	//
	// NOTE: launch with exec.Command, NOT exec.CommandContext. The VM is
	// long-running and must outlive the `sysbox apply` CLI invocation that
	// starts it (sysbox is invoked per-operation via subprocess; a later
	// `sysbox destroy` tears the VM down cold via socket/pidfile). Tying the
	// process to ctx would SIGKILL it the instant `sysbox apply` returns and
	// cancels its context, dropping the TAP fd and the guest's network.
	var cmd *exec.Cmd
	if vmState.netnsName != "" {
		cmd = exec.Command(ipBin, "netns", "exec", vmState.netnsName,
			s.firecrackerBin,
			"--config-file", vmState.cfgPath,
			"--api-sock", vmState.socket,
		)
	} else {
		cmd = exec.Command(s.firecrackerBin,
			"--config-file", vmState.cfgPath,
			"--api-sock", vmState.socket,
		)
	}
	// Redirect firecracker stdout (logs) and stderr (serial console) to a
	// per-VM log file rather than inheriting the parent's tty. Inheriting
	// caused the apply pipeline to never see EOF: the firecracker children
	// kept the write end of the parent's stderr pipe alive long after
	// 'sysbox apply' returned, so the shell appeared to hang at the end.
	logPath := filepath.Join(filepath.Dir(vmState.socket), "firecracker.log")
	logFD, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if logErr != nil {
		// Fall back to /dev/null rather than the parent's tty.
		logFD, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	cmd.Stdout = logFD
	cmd.Stderr = logFD
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := ctx.Err(); err != nil {
		_ = logFD.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = logFD.Close()
		return fmt.Errorf("start firecracker: %w", err)
	}
	// Parent's reference to the log fd is no longer needed; the child has
	// its own dup. Closing here prevents fd leaks across many VMs.
	_ = logFD.Close()

	startTime := processStartTime(cmd.Process.Pid)
	vm.mu.Lock()
	vm.cmd = cmd
	vm.pid = cmd.Process.Pid
	vm.startTime = startTime
	vm.started = true
	vm.exitCode = nil
	vm.exitError = ""
	vm.done = make(chan struct{})
	vm.mu.Unlock()
	// Reap immediately, including while readiness is pending or canceled.
	go reapVMProcess(vm, cmd)
	if hs, _ := h.Provider.(*HandleState); hs != nil {
		state := vm.snapshot()
		hs.PID = state.pid
		hs.PIDStart = state.startTime
		hs.Ready = false
		if hs.PIDFile != "" {
			if err := writeProcessAnchor(hs.PIDFile, processAnchor{PID: hs.PID, StartTime: hs.PIDStart, Socket: hs.Socket, VMID: h.ID}); err != nil {
				return fmt.Errorf("write firecracker process anchor: %w", err)
			}
		}
		hs.Ready = waitFirecrackerBoot(ctx, hs, 5*time.Second) == nil && !processReaped(vm)
		if hs.MetaPath != "" {
			if err := writeVMMetadata(hs.MetaPath, hs); err != nil {
				return fmt.Errorf("write firecracker metadata: %w", err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if state := vm.snapshot(); !state.started {
		return fmt.Errorf("firecracker VM %s exited during startup: %s", h.ID, vmExitReason(state, nil))
	}
	return nil
}

// StopNode signals the firecracker process to exit.
func (s *Substrate) StopNode(ctx context.Context, h substrate.NodeHandle) error {
	vm := lookupVM(h.ID)
	if vm == nil {
		return nil
	}
	if err := vm.lockOperation(ctx); err != nil {
		return err
	}
	defer vm.unlockOperation()
	return signalVMProcess(vm.snapshot(), syscall.SIGTERM)
}

func signalVMProcess(state vmProcessSnapshot, signal syscall.Signal) error {
	if state.destroyed || !state.started {
		return nil
	}
	if state.cmd != nil && state.cmd.Process != nil {
		err := state.cmd.Process.Signal(signal)
		if err == os.ErrProcessDone {
			return nil
		}
		return err
	}
	// Adopted processes have no exec.Cmd; never signal a recycled PID.
	if state.pid > 0 && state.startTime == "" {
		return fmt.Errorf("firecracker process %d has no ownership start time", state.pid)
	}
	if state.pid > 0 && processMatches(state.pid, state.startTime) {
		proc, err := os.FindProcess(state.pid)
		if err != nil {
			return err
		}
		defer proc.Release()
		return proc.Signal(signal)
	}
	return nil
}

// DestroyNode kills the VM process and cleans up files. Tolerates two
// invocation modes:
//   - hot: same process that created the VM (vmStore has the cmd handle).
//   - cold: a fresh CLI invocation after the in-memory map was lost
//     (e.g. `sysbox destroy` after the apply process exited). In that
//     case we reconstruct the conventional vm_dir from the VM ID and
//     kill whatever firecracker process is holding firecracker.sock.
func (s *Substrate) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	vm := lookupOrReserveVM(h.ID)
	if err := vm.lockOperation(ctx); err != nil {
		return err
	}
	defer vm.unlockOperation()
	state := vm.snapshot()
	if state.destroyed {
		return nil
	}
	vm.mu.Lock()
	vm.destroying = true
	vm.mu.Unlock()
	hotOwned := state.cmd != nil && state.cmd.Process != nil
	if err := signalVMProcess(state, syscall.SIGKILL); err != nil {
		return err
	}

	// Resolve vm_dir from typed handle if given, else fall back to the
	// substrate's conventional layout so cold destroys still clean up.
	dir := ""
	socket := ""
	vsock := ""
	if hs, _ := h.Provider.(*HandleState); hs != nil {
		dir = hs.VMDir
		socket = hs.Socket
		vsock = hs.VsockUDS
	}
	if dir == "" && h.ID != "" {
		dir = filepath.Join(s.rootfsDir, h.ID)
	}
	if dir != "" {
		if socket == "" {
			socket = ownedUnixSocketPath(dir, h.ID, "firecracker.sock", "fc")
		}
		if hotOwned {
			if err := waitAndRemoveHotProcessSocket(ctx, state, socket); err != nil {
				return err
			}
		} else if state.started && state.pid > 0 {
			if err := waitAndRemoveHotProcessSocket(ctx, state, socket); err != nil {
				return err
			}
		} else {
			if err := killOwnedStaleFirecracker(dir, h.ID, socket); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove firecracker VM directory %q: %w", dir, err)
		}
	}
	if vsock != "" {
		_ = os.Remove(vsock)
	}
	vm.mu.Lock()
	vm.destroyed = true
	vm.mu.Unlock()
	vmMu.Lock()
	if vmStore[h.ID] == vm {
		delete(vmStore, h.ID)
	}
	vmMu.Unlock()
	return nil
}

func waitAndRemoveHotProcessSocket(ctx context.Context, state vmProcessSnapshot, socketPath string) error {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		exited := false
		if state.done != nil {
			select {
			case <-state.done:
				exited = true
			default:
			}
		} else {
			exited = !processMatches(state.pid, state.startTime) || processIsZombie(state.pid)
		}
		if exited {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("firecracker hot process %d did not exit", state.pid)
		case <-state.done:
		case <-ticker.C:
		}
	}
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove hot firecracker socket: %w", err)
	}
	return nil
}

func ownedUnixSocketPath(runDir, vmID, filename, prefix string) string {
	candidate := filepath.Join(runDir, filename)
	if len(candidate) < 108 {
		return candidate
	}
	digest := sha256.Sum256([]byte(runDir + "\x00" + vmID + "\x00" + filename))
	return filepath.Join(os.TempDir(), fmt.Sprintf("sysbox-%s-%x.sock", prefix, digest[:8]))
}

// NodeStatus checks if the VM process is still alive.
// Hot path: in-process vmStore has the cmd handle, check via Signal(0).
// Cold path: process was started by a previous CLI invocation; reconstruct
// the socket path from HandleState and probe with pkill/pgrep.
func (s *Substrate) NodeStatus(ctx context.Context, h substrate.NodeHandle) (bool, error) {
	obs, err := s.ObserveNode(ctx, h)
	if err != nil {
		return false, err
	}
	return obs.Running, nil
}

func (s *Substrate) ObserveNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	vm := lookupOrReserveVM(h.ID)
	if err := vm.lockOperation(ctx); err != nil {
		return substrate.NodeObservation{}, err
	}
	defer vm.unlockOperation()
	return s.observeNode(h, vm.snapshot())
}

// Caller holds the VM transition gate; state is a detached process snapshot.
func (s *Substrate) observeNode(h substrate.NodeHandle, state vmProcessSnapshot) (substrate.NodeObservation, error) {
	now := time.Now().UTC()
	if state.destroyed {
		return substrate.NodeObservation{Status: substrate.NodeStatusMissing, ExternalID: h.ID, Reason: "VM destroyed", LastSeen: now}, nil
	}
	// Hot path: process in current process table.
	if state.cmd != nil && state.cmd.Process != nil {
		if err := state.cmd.Process.Signal(syscall.Signal(0)); err != nil || !state.started {
			return substrate.NodeObservation{
				Exists:     true,
				Running:    false,
				Healthy:    false,
				Status:     substrate.NodeStatusExited,
				PID:        state.pid,
				ExitCode:   state.exitCode,
				ExternalID: h.ID,
				Reason:     vmExitReason(state, err),
				LastSeen:   now,
			}, nil
		}
		ready := vmReady(h)
		return substrate.NodeObservation{
			Exists:     true,
			Running:    true,
			Healthy:    ready,
			Status:     substrate.NodeStatusRunning,
			PID:        state.pid,
			ExternalID: h.ID,
			Reason:     readyReason(ready),
			LastSeen:   now,
		}, nil
	}

	// Cold path: check if a firecracker process is still holding the socket.
	hs, _ := h.Provider.(*HandleState)
	if hs == nil || hs.Socket == "" {
		return substrate.NodeObservation{
			Exists:     false,
			Running:    false,
			Healthy:    false,
			Status:     substrate.NodeStatusMissing,
			ExternalID: h.ID,
			Reason:     "missing firecracker socket metadata",
			LastSeen:   now,
		}, nil
	}
	vmDirExists := hs.VMDir != ""
	if vmDirExists {
		if _, err := os.Stat(hs.VMDir); err != nil {
			vmDirExists = false
		}
	}
	socketExists := true
	if _, err := os.Stat(hs.Socket); err != nil {
		socketExists = false
	}
	pid := firecrackerPIDForSocket(hs.Socket)
	if pid == 0 && hs.PIDFile != "" {
		anchor := readProcessAnchor(hs.PIDFile)
		pid = anchor.PID
		if pid > 0 && !processMatches(pid, anchor.StartTime) {
			pid = 0
		}
	}
	if socketExists && pid > 0 {
		ready := vmReady(h)
		return substrate.NodeObservation{
			Exists:     true,
			Running:    true,
			Healthy:    ready,
			Status:     substrate.NodeStatusRunning,
			PID:        pid,
			ExternalID: h.ID,
			Reason:     readyReason(ready),
			LastSeen:   now,
		}, nil
	}
	status := substrate.NodeStatusMissing
	reason := "vm artifacts missing"
	if vmDirExists || socketExists {
		status = substrate.NodeStatusExited
		reason = "vm artifacts exist but process is not running"
	}
	return substrate.NodeObservation{
		Exists:     vmDirExists || socketExists,
		Running:    false,
		Healthy:    false,
		Status:     status,
		PID:        pid,
		ExternalID: h.ID,
		Reason:     reason,
		LastSeen:   now,
	}, nil
}

// AdoptNode reconnects this process to an already-running Firecracker VM.
// It does not recreate the original exec.Cmd, but it records the PID/socket in
// vmStore so later status/stop/destroy calls have an explicit ownership anchor.
func (s *Substrate) AdoptNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeHandle, error) {
	vm := lookupOrReserveVM(h.ID)
	if err := vm.lockOperation(ctx); err != nil {
		return h, err
	}
	defer vm.unlockOperation()
	state := vm.snapshot()
	if state.destroying || state.destroyed {
		return h, fmt.Errorf("firecracker adopt %s: VM is being destroyed", h.ID)
	}
	hs, _ := h.Provider.(*HandleState)
	if hs == nil || hs.Socket == "" {
		return h, fmt.Errorf("firecracker adopt %s: missing provider socket", h.ID)
	}
	obs, err := s.observeNode(h, state)
	if err != nil {
		return h, err
	}
	if !obs.Running {
		return h, fmt.Errorf("firecracker adopt %s: process not running", h.ID)
	}
	if state.cmd != nil {
		// Keep the active child and its reaper; adoption is idempotent.
		hs.PID, hs.PIDStart = state.pid, state.startTime
		return h, nil
	}
	pid := firecrackerPIDForSocket(hs.Socket)
	if pid == 0 && hs.PIDFile != "" {
		anchor := readProcessAnchor(hs.PIDFile)
		pid = anchor.PID
		if pid > 0 && !processMatches(pid, anchor.StartTime) {
			pid = 0
		}
	}
	if pid == 0 {
		return h, fmt.Errorf("firecracker adopt %s: process anchor not found", h.ID)
	}
	startTime := processStartTime(pid)
	if startTime == "" {
		return h, fmt.Errorf("firecracker adopt %s: process identity unavailable", h.ID)
	}
	vm.mu.Lock()
	vm.socket, vm.cfgPath, vm.netnsName = hs.Socket, hs.ConfigPath, hs.NetnsName
	vm.pid, vm.startTime, vm.started = pid, startTime, true
	vm.mu.Unlock()
	hs.PID = pid
	hs.PIDStart = startTime
	return h, nil
}

func firecrackerPIDForSocket(socketPath string) int {
	out, _ := exec.Command("pgrep", "-f", socketPath).CombinedOutput()
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	var pid int
	_, _ = fmt.Sscanf(fields[0], "%d", &pid)
	return pid
}

type processAnchor struct {
	PID       int    `json:"pid"`
	StartTime string `json:"start_time,omitempty"`
	Socket    string `json:"socket,omitempty"`
	VMID      string `json:"vm_id,omitempty"`
}

func writeProcessAnchor(path string, anchor processAnchor) error {
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(anchor, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0644)
}

func readProcessAnchor(path string) processAnchor {
	data, err := os.ReadFile(path)
	if err != nil {
		return processAnchor{}
	}
	var anchor processAnchor
	if err := json.Unmarshal(data, &anchor); err == nil && anchor.PID > 0 {
		return anchor
	}
	var pid int
	_, _ = fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
	return processAnchor{PID: pid}
}

func readPIDFile(path string) int {
	return readProcessAnchor(path).PID
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func processStartTime(pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) < 22 {
		return ""
	}
	return fields[21]
}

func processMatches(pid int, startTime string) bool {
	if !processAlive(pid) {
		return false
	}
	if startTime == "" {
		return true
	}
	return processStartTime(pid) == startTime
}

func reapVMProcess(vm *vmProcess, cmd *exec.Cmd) {
	err := cmd.Wait()
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.cmd == cmd {
		vm.started = false
		code := cmd.ProcessState.ExitCode()
		vm.exitCode = &code
		vm.exitError = ""
		if err != nil {
			vm.exitError = err.Error()
		}
		if vm.done != nil {
			close(vm.done)
		}
	}
}

func processReaped(vm *vmProcess) bool {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	return !vm.started && vm.pid > 0
}

func vmExitReason(state vmProcessSnapshot, err error) string {
	if err != nil {
		return err.Error()
	}
	if state.exitError != "" {
		return state.exitError
	}
	if !state.started {
		return "firecracker process exited"
	}
	return "firecracker process not running"
}

func vmReady(h substrate.NodeHandle) bool {
	hs, _ := h.Provider.(*HandleState)
	if hs == nil {
		return true
	}
	if hs.VsockUDS == "" {
		return true
	}
	return hs.Ready
}

func readyReason(ready bool) string {
	if ready {
		return "process alive and ready"
	}
	return "process alive but guest readiness not confirmed"
}

func waitFirecrackerBoot(ctx context.Context, hs *HandleState, timeout time.Duration) error {
	if hs == nil || hs.VsockUDS == "" {
		return nil
	}
	conn := transport.NewVsockConnection(hs.VsockUDS, hs.VsockPort)
	return conn.WaitReady(ctx, timeout)
}

func writeVMMetadata(path string, hs *HandleState) error {
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(map[string]any{
		"managed_by":   "sysbox",
		"vm_id":        filepath.Base(hs.VMDir),
		"socket":       hs.Socket,
		"config_path":  hs.ConfigPath,
		"pid_file":     hs.PIDFile,
		"pid":          hs.PID,
		"pid_start":    hs.PIDStart,
		"ready":        hs.Ready,
		"crash_policy": hs.CrashPolicy,
		"updated_at":   time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ── Firecracker config types ────────────────────────────────────────────────

type fcConfig struct {
	BootSource        fcBootSource         `json:"boot-source"`
	MachineConfig     fcMachineConfig      `json:"machine-config"`
	Drives            []fcDrive            `json:"drives"`
	NetworkInterfaces []fcNetworkInterface `json:"network-interfaces,omitempty"`
	Vsock             *fcVsock             `json:"vsock,omitempty"`
}

// fcVsock declares a virtio-vsock device. uds_path is the host-side Unix
// Domain Socket; firecracker speaks its multiplexing protocol on this socket
// (CONNECT <port>\n → OK <hostport>\n).
type fcVsock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type fcBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type fcMachineConfig struct {
	VcpuCount int `json:"vcpu_count"`
	MemSizeMB int `json:"mem_size_mib"`
}

type fcDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsReadOnly   bool   `json:"is_read_only"`
	IsRootDevice bool   `json:"is_root_device"`
}

type fcNetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac,omitempty"`
	HostDevName string `json:"host_dev_name"`
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func sanitizeName(name string) string {
	s := filepath.Base(name)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Sync()
}

// dockerExportToExt4 creates an ext4 image from a Docker image.
// It requires a privileged container (via docker run --privileged) to mount.
func dockerExportToExt4(ctx context.Context, dockerRef, outPath string) error {
	tmpTar := outPath + ".tar"
	createCmd := exec.CommandContext(ctx, "docker", "create", dockerRef, "sleep", "infinity")
	out, err := createCmd.Output()
	if err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	cid := string(out)[:12]
	defer func() {
		if err := exec.Command("docker", "rm", "-f", cid).Run(); err != nil {
			slog.Debug("cleanup helper container", "cid", cid, "error", err)
		}
	}()

	exportCmd := exec.CommandContext(ctx, "docker", "export", cid, "-o", tmpTar)
	if err := exportCmd.Run(); err != nil {
		return fmt.Errorf("docker export: %w", err)
	}
	defer os.Remove(tmpTar)

	buildCmd := exec.CommandContext(ctx, "docker", "run", "--rm",
		"-v", filepath.Dir(outPath)+":/out",
		"-v", tmpTar+":/rootfs.tar",
		"--privileged",
		"ubuntu:24.04",
		"bash", "-c",
		`dd if=/dev/zero of=/out/`+filepath.Base(outPath)+` bs=1M count=200 2>/dev/null && \
     mkfs.ext4 -F /out/`+filepath.Base(outPath)+` 2>/dev/null && \
     mkdir -p /mnt/rootfs && \
     mount -o loop /out/`+filepath.Base(outPath)+` /mnt/rootfs && \
     cd /mnt/rootfs && \
     tar -xf /rootfs.tar && \
     cd / && umount -l /mnt/rootfs`,
	)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build ext4: %w\n%s", err, out)
	}
	return nil
}

// killStaleFirecracker kills any Firecracker process that has the given
// API socket open. This frees the TAP fd held by a previous failed apply.
func killOwnedStaleFirecracker(vmDir, vmID, socketPath string) error {
	// Use fuser to find PIDs with the socket open, or fall back to pkill.
	if _, err := os.Stat(socketPath); err != nil {
		return nil // no socket — no stale process
	}
	anchor := readProcessAnchor(filepath.Join(vmDir, "firecracker.pid"))
	if anchor.PID <= 0 || anchor.StartTime == "" || anchor.VMID != vmID || anchor.Socket != socketPath {
		return fmt.Errorf("firecracker refuses stale process cleanup without matching ownership anchor for %q", vmID)
	}
	if !processMatches(anchor.PID, anchor.StartTime) {
		return os.Remove(socketPath)
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", anchor.PID))
	if err == nil && len(cmdline) == 0 && processIsZombie(anchor.PID) {
		return os.Remove(socketPath)
	}
	if err != nil || !strings.Contains(strings.ReplaceAll(string(cmdline), "\x00", " "), socketPath) {
		return fmt.Errorf("firecracker stale process command line ownership mismatch for %q", vmID)
	}
	proc, err := os.FindProcess(anchor.PID)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill owned stale firecracker %d: %w", anchor.PID, err)
	}
	// Give the kernel a moment to release TAP fds.
	time.Sleep(300 * time.Millisecond)
	// Remove the stale socket file.
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

func processIsZombie(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	closeParen := strings.LastIndexByte(string(raw), ')')
	return closeParen >= 0 && len(raw) > closeParen+2 && raw[closeParen+2] == 'Z'
}
