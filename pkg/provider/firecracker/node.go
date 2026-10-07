package firecracker

import (
	"context"
	"crypto/ed25519"
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
	Attachments []managedAttachment `json:"attachments"`

	Version      int    `json:"version"`
	ManifestPath string `json:"manifest_path"`
	OwnerUID     string `json:"owner_uid"`
	ResourceUID  string `json:"resource_uid"`
	TopologyUID  string `json:"topology_uid,omitempty"`
	Generation   int64  `json:"generation"`
	VMDir        string `json:"vm_dir,omitempty"`
	Socket       string `json:"socket,omitempty"`
	ConfigPath   string `json:"config_path,omitempty"`
	PID          int    `json:"pid,omitempty"`
	PIDStart     string `json:"pid_start,omitempty"`
	Ready        bool   `json:"ready,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	ExitError    string `json:"exit_error,omitempty"`
	CrashPolicy  string `json:"crash_policy,omitempty"`

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
	vmStore = map[string]*vmProcess{} // canonical generation directory → vmProcess
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
	op, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || op.Validate() != nil {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker create requires durable operation identity")
	}
	root, err := s.canonicalRoot()
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d", root, op.TopologyUID, op.ResourceUID, op.Generation)))
	return s.createNodeWithID(ctx, spec, fmt.Sprintf("fc-%x", digest[:12]))
}

func (s *Substrate) createNodeWithID(ctx context.Context, spec substrate.NodeSpec, vmID string) (substrate.NodeHandle, error) {
	op, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || op.Validate() != nil {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker create requires durable operation identity")
	}
	memMB := 512
	if spec.Memory != "" {
		parsed, err := parseMemoryMiB(spec.Memory)
		if err != nil {
			return substrate.NodeHandle{}, err
		}
		memMB = parsed
	}

	root, err := s.canonicalRoot()
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if !validIdentityPart(vmID) {
		return substrate.NodeHandle{}, fmt.Errorf("invalid firecracker VM identity")
	}
	if !validIdentityPart(op.OwnerUID) || !validIdentityPart(op.ResourceUID) || !validIdentityPart(op.TopologyUID) {
		return substrate.NodeHandle{}, fmt.Errorf("invalid firecracker operation identity")
	}
	runDir := filepath.Join(root, op.TopologyUID, op.ResourceUID, fmt.Sprint(op.Generation))
	vm := lookupOrReserveVM(runDir)
	if err := vm.lockOperation(ctx); err != nil {
		return substrate.NodeHandle{}, err
	}
	defer vm.unlockOperation()
	state := vm.snapshot()
	if state.started || state.destroying || state.destroyed {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker VM %s already running or being destroyed", vmID)
	}
	prospective := substrate.NodeHandle{ID: vmID, Provider: &HandleState{VMDir: runDir}}
	unlock, err := s.lockHostOperation(ctx, prospective)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	defer unlock()
	if _, err := os.Lstat(runDir); err == nil {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker existing manifest or orphan directory requires discovery before create: %s", runDir)
	} else if !os.IsNotExist(err) {
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

	imageDigest, err := digestFile(imagePath)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if spec.Image.Identity.Digest != "" && spec.Image.Identity.Digest != imageDigest {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker rootfs digest conflict")
	}
	if err := os.MkdirAll(runDir, 0700); err != nil {
		return substrate.NodeHandle{}, err
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
		if s.grantPublicKeyFile != "" {
			key, err := os.ReadFile(s.grantPublicKeyFile)
			if err != nil {
				return substrate.NodeHandle{}, fmt.Errorf("read guest grant trust key: %w", err)
			}
			if len(key) != ed25519.PublicKeySize {
				return substrate.NodeHandle{}, fmt.Errorf("guest grant trust key must be Ed25519 public key")
			}
			initCfg.GuestGrantPublicKey = key
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

	socketPath := ownedUnixSocketPath(runDir, vmID, "firecracker.sock", "fc")
	if err := ensureOwnedSocketDirectory(socketPath); err != nil {
		return substrate.NodeHandle{}, err
	}
	if _, err := os.Lstat(socketPath); err == nil {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker orphan socket blocks creation")
	} else if !os.IsNotExist(err) {
		return substrate.NodeHandle{}, err
	}
	cfgPath := filepath.Join(runDir, "vm_config.json")

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
		if _, err := os.Lstat(vsockUDS); err == nil {
			return substrate.NodeHandle{}, fmt.Errorf("firecracker orphan vsock blocks creation")
		} else if !os.IsNotExist(err) {
			return substrate.NodeHandle{}, err
		}
		vsockCID = s.allocCID()
		cfg.Vsock = &fcVsock{GuestCID: vsockCID, UDSPath: vsockUDS}
	}

	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("marshal VM config: %w", err)
	}
	if err := atomicWriteFile(cfgPath, cfgData, 0600); err != nil {
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
		CrashPolicy: "observe_only",
	}
	conn := substrate.ConnInfo{}
	if vsockUDS != "" {
		hs.VsockUDS = vsockUDS
		hs.VsockCID = vsockCID
		hs.VsockPort = uint32(8901)
		conn.Kind = substrate.ConnKindVsock
		conn.Endpoint = fmt.Sprintf("%s:%d", vsockUDS, hs.VsockPort)
	}
	m := newManifest(ctx, root, vmID, runDir, hs, imageDigest)
	if err := refreshConfigDigest(m); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := saveManifest(m); err != nil {
		return substrate.NodeHandle{}, err
	}
	return substrate.NodeHandle{
		ID:         vmID,
		Generation: hs.Generation,
		Provider:   hs,
		Conn:       conn,
	}, nil
}

// StartNode launches the Firecracker process with the completed config.
// This must be called after all AttachNIC calls are done.
func (s *Substrate) StartNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.startManifestNode(ctx, h)
}

// StopNode signals the firecracker process to exit.
func (s *Substrate) StopNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.signalManifestNode(ctx, h, syscall.SIGTERM)
}

// DestroyNode validates durable ownership, signals through a pidfd and confirms
// exit before removing a generation. Cold and hot callers share this path.
func (s *Substrate) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	return s.destroyManifestNode(ctx, h)
}

func ownedUnixSocketPath(runDir, vmID, filename, prefix string) string {
	candidate := filepath.Join(runDir, filename)
	if len(candidate) < 108 {
		return candidate
	}
	digest := sha256.Sum256([]byte(runDir + "\x00" + vmID + "\x00" + filename))
	return filepath.Join(os.TempDir(), fmt.Sprintf("sysbox-sockets-%d", os.Geteuid()), fmt.Sprintf("%s-%x.sock", prefix, digest[:16]))
}

// NodeStatus reports liveness from the same durable ownership probe as ObserveNode.
func (s *Substrate) NodeStatus(ctx context.Context, h substrate.NodeHandle) (bool, error) {
	obs, err := s.ObserveNode(ctx, h)
	if err != nil {
		return false, err
	}
	return obs.Running, nil
}

func (s *Substrate) ObserveNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	return s.observeManifestNode(ctx, h)
}

// Caller holds the VM transition gate; state is a detached process snapshot.

// AdoptNode reconnects this process to an already-running Firecracker VM.
// It does not recreate the original exec.Cmd, but it records the PID/socket in
// vmStore so later status/stop/destroy calls have an explicit ownership anchor.
func (s *Substrate) AdoptNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeHandle, error) {
	return s.adoptManifestNode(ctx, h)
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
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
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

func waitFirecrackerBoot(ctx context.Context, hs *HandleState, timeout time.Duration) error {
	if hs == nil || hs.VsockUDS == "" {
		return nil
	}
	conn := transport.NewVsockConnection(hs.VsockUDS, hs.VsockPort)
	return conn.WaitReady(ctx, timeout)
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

func processIsZombie(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	closeParen := strings.LastIndexByte(string(raw), ')')
	return closeParen >= 0 && len(raw) > closeParen+2 && raw[closeParen+2] == 'Z'
}
