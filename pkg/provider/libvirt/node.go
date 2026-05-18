package libvirt

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oslab/sysbox/pkg/substrate"
)

// HandleState is the libvirt substrate's typed NodeHandle.Provider payload.
type HandleState struct {
	DomainName  string         `json:"domain_name"`
	VMDir       string         `json:"vm_dir"`
	DiskPath    string         `json:"disk_path"`
	VCPUs       int            `json:"vcpus"`
	MemoryMiB   int            `json:"memory_mib"`
	MachineType string         `json:"machine_type"`
	Bridges     []BridgeAttach `json:"bridges,omitempty"`
	SSHIP       string         `json:"ssh_ip,omitempty"`
	SSHUser     string         `json:"ssh_user,omitempty"`
	SSHPass     string         `json:"ssh_pass,omitempty"`
	SSHKey      string         `json:"ssh_key,omitempty"`
}

func (s *Substrate) PrepareImage(_ context.Context, spec substrate.ImageSpec) (substrate.ImageRef, error) {
	if spec.QCow2 == "" {
		return substrate.ImageRef{}, fmt.Errorf("libvirt substrate requires ImageSpec.QCow2")
	}
	if _, err := os.Stat(spec.QCow2); err != nil {
		return substrate.ImageRef{}, fmt.Errorf("libvirt: qcow2 image not found: %w", err)
	}
	return substrate.ImageRef{ID: spec.QCow2, Repository: spec.QCow2}, nil
}

func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	baseImage := spec.Image.ID
	if baseImage == "" {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: no qcow2 base image in ImageRef")
	}

	pc, _ := spec.ProviderConfig.(*Config)
	if pc == nil {
		pc = &Config{VCPUs: 1, Memory: "512", MachineType: "q35", SSHUser: "root"}
	}
	mem, err := parseMiB(pc.Memory)
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: invalid memory %q: %w", pc.Memory, err)
	}

	// Tear down any stale domain with the same name.
	_, _ = exec.CommandContext(ctx, "virsh", "destroy", spec.Name).CombinedOutput()
	_, _ = exec.CommandContext(ctx, "virsh", "undefine", spec.Name).CombinedOutput()

	vmDir, err := os.MkdirTemp("", "sysbox-lv-"+spec.Name+"-*")
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: create vm dir: %w", err)
	}

	diskPath := filepath.Join(vmDir, "disk.qcow2")
	qiArgs := []string{"create", "-f", "qcow2", "-b", baseImage, "-F", "qcow2", diskPath}
	if pc.DiskSize != "" {
		qiArgs = append(qiArgs, pc.DiskSize)
	}
	if out, err := exec.CommandContext(ctx, "qemu-img", qiArgs...).CombinedOutput(); err != nil {
		_ = os.RemoveAll(vmDir)
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: qemu-img create: %w\n%s", err, out)
	}

	machine := pc.MachineType
	if machine == "" {
		machine = "q35"
	}
	hs := &HandleState{
		DomainName:  spec.Name,
		VMDir:       vmDir,
		DiskPath:    diskPath,
		VCPUs:       pc.VCPUs,
		MemoryMiB:   mem,
		MachineType: machine,
		SSHUser:     pc.SSHUser,
		SSHPass:     pc.SSHPass,
		SSHKey:      pc.SSHKey,
	}
	return substrate.NodeHandle{
		ID:       spec.Name,
		Provider: hs,
		Conn:     substrate.ConnInfo{Kind: substrate.ConnKindSSH},
	}, nil
}

func (s *Substrate) StartNode(ctx context.Context, h substrate.NodeHandle) error {
	hs := hsFrom(h)

	xmlStr, err := GenerateDomainXML(DomainSpec{
		Name:        hs.DomainName,
		VCPUs:       hs.VCPUs,
		MemoryMiB:   hs.MemoryMiB,
		MachineType: hs.MachineType,
		DiskPath:    hs.DiskPath,
		Bridges:     hs.Bridges,
	})
	if err != nil {
		return err
	}

	xmlPath := filepath.Join(hs.VMDir, "domain.xml")
	if err := os.WriteFile(xmlPath, []byte(xmlStr), 0o644); err != nil {
		return fmt.Errorf("libvirt: write domain xml: %w", err)
	}
	if out, err := exec.CommandContext(ctx, "virsh", "define", xmlPath).CombinedOutput(); err != nil {
		return fmt.Errorf("libvirt: virsh define: %w\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "virsh", "start", hs.DomainName).CombinedOutput(); err != nil {
		_, _ = exec.Command("virsh", "undefine", hs.DomainName).CombinedOutput()
		return fmt.Errorf("libvirt: virsh start: %w\n%s", err, out)
	}
	return nil
}

func (s *Substrate) StopNode(ctx context.Context, h substrate.NodeHandle) error {
	hs := hsFrom(h)
	if _, err := exec.CommandContext(ctx, "virsh", "shutdown", hs.DomainName).CombinedOutput(); err != nil {
		_, _ = exec.CommandContext(ctx, "virsh", "destroy", hs.DomainName).CombinedOutput()
	}
	return nil
}

func (s *Substrate) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	hs := hsFrom(h)
	_, _ = exec.CommandContext(ctx, "virsh", "destroy", hs.DomainName).CombinedOutput()
	if out, err := exec.CommandContext(ctx, "virsh", "undefine", hs.DomainName).CombinedOutput(); err != nil {
		if active, _ := domainActive(ctx, hs.DomainName); active {
			return fmt.Errorf("libvirt: virsh undefine: %w\n%s", err, out)
		}
	}
	if hs.VMDir != "" {
		_ = os.RemoveAll(hs.VMDir)
	}
	return nil
}

func (s *Substrate) NodeStatus(ctx context.Context, h substrate.NodeHandle) (bool, error) {
	hs := hsFrom(h)
	out, err := exec.CommandContext(ctx, "virsh", "domstate", hs.DomainName).CombinedOutput()
	if err != nil {
		return false, nil
	}
	return strings.Contains(string(out), "running"), nil
}

func (s *Substrate) PrepareHandle(_ context.Context, h *substrate.NodeHandle, _ any, _ substrate.StateReader) error {
	hs := hsFrom(*h)
	if hs.SSHIP == "" {
		return nil // SSH IP not yet known; provisioners will configure it
	}
	h.Conn = substrate.ConnInfo{
		Kind:     substrate.ConnKindSSH,
		Endpoint: hs.SSHIP + ":22",
		User:     hs.SSHUser,
	}
	return nil
}

func (s *Substrate) MarshalProviderState(h substrate.NodeHandle) (json.RawMessage, error) {
	hs, ok := h.Provider.(*HandleState)
	if !ok || hs == nil {
		return nil, nil
	}
	return json.Marshal(hs)
}

func (s *Substrate) UnmarshalProviderState(data json.RawMessage) (any, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var hs HandleState
	if err := json.Unmarshal(data, &hs); err != nil {
		return nil, err
	}
	return &hs, nil
}

// WaitForSSH polls addr:22 until reachable or ctx/timeout expires.
func WaitForSSH(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr+":22", 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("libvirt: SSH on %s not reachable after %s", addr, timeout)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func hsFrom(h substrate.NodeHandle) *HandleState {
	if hs, ok := h.Provider.(*HandleState); ok && hs != nil {
		return hs
	}
	return &HandleState{DomainName: h.ID}
}

func parseMiB(s string) (int, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	for _, sfx := range []string{"GIB", "GB", "G"} {
		if strings.HasSuffix(s, sfx) {
			n, err := strconv.Atoi(strings.TrimSuffix(s, sfx))
			return n * 1024, err
		}
	}
	for _, sfx := range []string{"MIB", "MB", "M"} {
		if strings.HasSuffix(s, sfx) {
			return strconv.Atoi(strings.TrimSuffix(s, sfx))
		}
	}
	return strconv.Atoi(s)
}

func domainActive(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, "virsh", "domstate", name).CombinedOutput()
	if err != nil {
		return false, nil
	}
	state := strings.TrimSpace(string(out))
	return state != "" && !strings.Contains(state, "shut off"), nil
}
