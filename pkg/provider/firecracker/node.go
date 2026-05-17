package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

// vmProcess tracks a running Firecracker VM process.
type vmProcess struct {
	mu        sync.Mutex
	cmd       *exec.Cmd
	pid       int
	socket    string // API socket path
	vmID      string
	rootfs    string // per-VM rootfs copy
	cfgPath   string // VM config JSON path
	netnsName string // network netns to run FC inside (empty = root netns)
	started   bool   // true after StartNode
}

// HandleState is the firecracker-substrate's typed NodeHandle.Provider payload.
// Persisted via MarshalProviderState so cold-destroy and drift refresh can
// rebuild the VM's working directory without rediscovery.
type HandleState struct {
	VMDir      string `json:"vm_dir,omitempty"`
	Socket     string `json:"socket,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`

	VsockUDS  string `json:"vsock_uds,omitempty"`
	VsockCID  uint32 `json:"vsock_cid,omitempty"`
	VsockPort uint32 `json:"vsock_port,omitempty"`

	NICCount int    `json:"nic_count,omitempty"`
	TapName  string `json:"tap_name,omitempty"`

	NetnsName string `json:"netns_name,omitempty"`

	// SSHIP/SSHPort are populated by runtime as a fallback for VMs whose
	// rootfs lacks sysbox-init (no vsock channel). W1-PR-06 will move this
	// behind Substrate.Connection().
	SSHIP   string `json:"ssh_ip,omitempty"`
	SSHPort string `json:"ssh_port,omitempty"`
}

var (
	vmMu    sync.Mutex
	vmStore = map[string]*vmProcess{} // vm_id → vmProcess
)

// PrepareImage builds a rootfs ext4 image from a Docker image or returns
// a direct rootfs path unchanged.
func (s *Substrate) PrepareImage(ctx context.Context, spec substrate.ImageSpec) (substrate.ImageRef, error) {
	if spec.Rootfs != "" {
		return substrate.ImageRef{
			ID:         spec.Rootfs,
			Repository: spec.Rootfs,
		}, nil
	}

	if spec.DockerRef == "" {
		return substrate.ImageRef{}, fmt.Errorf("firecracker image: either rootfs or docker_ref required")
	}

	outPath := filepath.Join(s.rootfsDir, sanitizeName(spec.DockerRef)+".ext4")
	if _, err := os.Stat(outPath); err == nil {
		return substrate.ImageRef{ID: outPath, Repository: outPath}, nil
	}

	if err := os.MkdirAll(s.rootfsDir, 0755); err != nil {
		return substrate.ImageRef{}, fmt.Errorf("create rootfs dir: %w", err)
	}

	if err := dockerExportToExt4(spec.DockerRef, outPath); err != nil {
		return substrate.ImageRef{}, fmt.Errorf("build rootfs from %s: %w", spec.DockerRef, err)
	}

	return substrate.ImageRef{ID: outPath, Repository: outPath}, nil
}

// CreateNode prepares the VM config but does NOT start the VM yet.
// NICs are added via AttachNIC, then the VM is started via StartNode.
// This two-phase approach is needed because Firecracker requires all
// network interfaces to be declared in the boot config — no hot-plug.
func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	vmID := spec.Name // e.g. "sysbox-node_attack"
	runDir := filepath.Join(s.rootfsDir, vmID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("create VM run dir: %w", err)
	}

	// Kill any leftover Firecracker process from a previous failed run.
	// A stale FC process holds the TAP fd open, preventing TAP reuse.
	killStaleFirecracker(filepath.Join(runDir, "firecracker.sock"))

	// Copy the base rootfs so each VM has its own writable copy.
	imagePath := spec.Image.Repository
	vmRootfs := filepath.Join(runDir, "rootfs.ext4")

	// Rootfs override: if spec.Rootfs is set, use it directly.
	if spec.Rootfs != "" {
		imagePath = spec.Rootfs
	}

	if err := copyFile(imagePath, vmRootfs); err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("copy rootfs: %w", err)
	}

	// Inject sysbox-init into the per-VM rootfs copy and build a per-VM
	// config drive. Both are best-effort: if sysbox-init was not built into
	// the host binary (placeholder embed), or mkfs.ext4/mount aren't
	// available, we fall back to the legacy "init=/init" path and let the
	// rootfs handle everything (compatible with the pre-phase-C behaviour).
	sysboxInitEnabled := true
	configDrivePath := filepath.Join(runDir, "config.ext4")
	if err := injectInitBinary(vmRootfs); err != nil {
		fmt.Printf("[firecracker] sysbox-init disabled for %s: %v\n", vmID, err)
		sysboxInitEnabled = false
	} else {
		nodeName := strings.TrimPrefix(vmID, "sysbox-")
		initCfg := VMInitConfig{
			Hostname:  nodeName,
			Env:       spec.Env,
			ChainInit: spec.ChainInit,
		}
		if err := buildConfigDrive(configDrivePath, initCfg); err != nil {
			fmt.Printf("[firecracker] config drive build failed for %s: %v\n", vmID, err)
			sysboxInitEnabled = false
		}
	}

	// Determine kernel path: per-node override > substrate default.
	kernelPath := s.kernelPath
	if spec.Kernel != "" {
		kernelPath = spec.Kernel
	}

	// Generate initial VM config (no NICs yet — added by AttachNIC).
	vcpus := 2
	if spec.VCPUs > 0 {
		vcpus = spec.VCPUs
	}
	memMB := 512
	if spec.Memory != "" {
		fmt.Sscanf(spec.Memory, "%d", &memMB)
	}

	socketPath := filepath.Join(runDir, "firecracker.sock")
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
		vsockUDS = filepath.Join(runDir, "vsock.sock")
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

	vm := &vmProcess{
		vmID:    vmID,
		socket:  socketPath,
		rootfs:  vmRootfs,
		cfgPath: cfgPath,
		started: false,
	}

	vmMu.Lock()
	vmStore[vmID] = vm
	vmMu.Unlock()

	hs := &HandleState{
		VMDir:      runDir,
		Socket:     socketPath,
		ConfigPath: cfgPath,
	}
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
	vmMu.Lock()
	vm, ok := vmStore[h.ID]
	vmMu.Unlock()
	if !ok {
		return fmt.Errorf("VM %s not found", h.ID)
	}
	if vm.started {
		return nil // already running
	}

	// Remove stale socket if present.
	os.Remove(vm.socket) //nolint:errcheck

	// If a network netns is set, run Firecracker inside it so it can access
	// the TAP device and bridge that live in that netns.
	var cmd *exec.Cmd
	if vm.netnsName != "" {
		cmd = exec.CommandContext(ctx, ipBin, "netns", "exec", vm.netnsName,
			s.firecrackerBin,
			"--config-file", vm.cfgPath,
			"--api-sock", vm.socket,
		)
	} else {
		cmd = exec.CommandContext(ctx, s.firecrackerBin,
			"--config-file", vm.cfgPath,
			"--api-sock", vm.socket,
		)
	}
	// Redirect firecracker stdout (logs) and stderr (serial console) to a
	// per-VM log file rather than inheriting the parent's tty. Inheriting
	// caused the apply pipeline to never see EOF: the firecracker children
	// kept the write end of the parent's stderr pipe alive long after
	// 'sysbox apply' returned, so the shell appeared to hang at the end.
	logPath := filepath.Join(filepath.Dir(vm.socket), "firecracker.log")
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

	if err := cmd.Start(); err != nil {
		_ = logFD.Close()
		return fmt.Errorf("start firecracker: %w", err)
	}
	// Parent's reference to the log fd is no longer needed; the child has
	// its own dup. Closing here prevents fd leaks across many VMs.
	_ = logFD.Close()

	vm.cmd = cmd
	vm.pid = cmd.Process.Pid
	vm.started = true
	return nil
}

// StopNode signals the firecracker process to exit.
func (s *Substrate) StopNode(_ context.Context, h substrate.NodeHandle) error {
	vmMu.Lock()
	vm, ok := vmStore[h.ID]
	vmMu.Unlock()
	if !ok || vm.cmd == nil || vm.cmd.Process == nil {
		return nil
	}
	return vm.cmd.Process.Signal(syscall.SIGTERM)
}

// DestroyNode kills the VM process and cleans up files. Tolerates two
// invocation modes:
//   - hot: same process that created the VM (vmStore has the cmd handle).
//   - cold: a fresh CLI invocation after the in-memory map was lost
//     (e.g. `sysbox destroy` after the apply process exited). In that
//     case we reconstruct the conventional vm_dir from the VM ID and
//     kill whatever firecracker process is holding firecracker.sock.
func (s *Substrate) DestroyNode(_ context.Context, h substrate.NodeHandle) error {
	vmMu.Lock()
	vm, ok := vmStore[h.ID]
	delete(vmStore, h.ID)
	vmMu.Unlock()

	if ok && vm.cmd != nil && vm.cmd.Process != nil {
		_ = vm.cmd.Process.Signal(syscall.SIGKILL)
		_ = vm.cmd.Wait()
	}

	// Resolve vm_dir from typed handle if given, else fall back to the
	// substrate's conventional layout so cold destroys still clean up.
	dir := ""
	if hs, _ := h.Provider.(*HandleState); hs != nil {
		dir = hs.VMDir
	}
	if dir == "" && h.ID != "" {
		dir = filepath.Join(s.rootfsDir, h.ID)
	}
	if dir != "" {
		killStaleFirecracker(filepath.Join(dir, "firecracker.sock"))
		_ = os.RemoveAll(dir)
	}
	return nil
}

// NodeStatus checks if the VM process is still alive.
func (s *Substrate) NodeStatus(_ context.Context, h substrate.NodeHandle) (bool, error) {
	vmMu.Lock()
	vm, ok := vmStore[h.ID]
	vmMu.Unlock()
	if !ok || vm.cmd == nil || vm.cmd.Process == nil {
		return false, nil
	}
	if err := vm.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		return false, nil
	}
	return true, nil
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
func dockerExportToExt4(dockerRef, outPath string) error {
	tmpTar := outPath + ".tar"
	createCmd := exec.Command("docker", "create", dockerRef, "sleep", "infinity")
	out, err := createCmd.Output()
	if err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	cid := string(out)[:12]
	defer exec.Command("docker", "rm", "-f", cid).Run() //nolint:errcheck

	exportCmd := exec.Command("docker", "export", cid, "-o", tmpTar)
	if err := exportCmd.Run(); err != nil {
		return fmt.Errorf("docker export: %w", err)
	}
	defer os.Remove(tmpTar)

	buildCmd := exec.Command("docker", "run", "--rm",
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
func killStaleFirecracker(socketPath string) {
	// Use fuser to find PIDs with the socket open, or fall back to pkill.
	if _, err := os.Stat(socketPath); err != nil {
		return // no socket — no stale process
	}
	// pkill on the socket path matches firecracker processes using it.
	exec.Command("pkill", "-9", "-f", socketPath).Run() //nolint:errcheck
	// Give the kernel a moment to release TAP fds.
	time.Sleep(300 * time.Millisecond)
	// Remove the stale socket file.
	os.Remove(socketPath) //nolint:errcheck
}
