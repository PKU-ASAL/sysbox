package libvirt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/substrate"
)

// HandleState is the libvirt substrate's typed NodeHandle.Provider payload.
type HandleState struct {
	DomainName       string                         `json:"domain_name"`
	DomainUUID       string                         `json:"domain_uuid,omitempty"`
	VMDir            string                         `json:"vm_dir"`
	DiskPath         string                         `json:"disk_path"`
	VCPUs            int                            `json:"vcpus"`
	MemoryMiB        int                            `json:"memory_mib"`
	MachineType      string                         `json:"machine_type"`
	Bridges          []BridgeAttach                 `json:"bridges,omitempty"`
	SSHIP            string                         `json:"ssh_ip,omitempty"`
	SSHUser          string                         `json:"ssh_user,omitempty"`
	SSHPass          string                         `json:"-"`
	SSHKey           string                         `json:"ssh_key,omitempty"`
	SSHAuthorizedKey string                         `json:"-"`
	NetworkInit      substrate.GuestNetworkInitMode `json:"network_init"`
	SeedISO          string                         `json:"seed_iso,omitempty"`
}

func (s *Substrate) ResolveImage(_ context.Context, source substrate.ArtifactSource) (substrate.ArtifactHandle, error) {
	if source.Kind != substrate.ArtifactQCow2 {
		return substrate.ArtifactHandle{}, fmt.Errorf("libvirt substrate requires artifact kind %q", substrate.ArtifactQCow2)
	}
	effective := source.ResolvedSource
	if effective == "" {
		effective = source.Source
	}
	resolved, err := artifact.New().ResolveIdentity(artifact.IdentitySpec{Kind: source.Kind, Source: effective, ExpectedDigest: source.ExpectedDigest, Architecture: source.Architecture, GuestFamily: source.GuestFamily, Metadata: source.Metadata})
	if err != nil {
		return substrate.ArtifactHandle{}, fmt.Errorf("libvirt: resolve qcow2: %w", err)
	}
	resolved.Identity.Source = source.Source
	return substrate.ArtifactHandle{Identity: resolved.Identity, ID: resolved.Path}, nil
}

func (s *Substrate) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	baseImage := spec.Image.ID
	if baseImage == "" {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: no qcow2 base image in artifact handle")
	}

	pc, _ := spec.ProviderConfig.(*Config)
	if pc == nil {
		pc = &Config{VCPUs: 1, Memory: "512", MachineType: "q35", SSHUser: "root"}
	}
	mem, err := parseMiB(pc.Memory)
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: invalid memory %q: %w", pc.Memory, err)
	}

	// Only tear down stale domains that were previously created by sysbox.
	// Destroying an arbitrary domain with the same name is dangerous on
	// shared libvirt hosts (e.g. production VMs named "db" or "gateway").
	if active, _ := domainActive(ctx, spec.Name); active {
		// Verify the domain is sysbox-managed before destroying it.
		if !isSysboxManaged(ctx, spec.Name) {
			return substrate.NodeHandle{}, fmt.Errorf("libvirt: domain %q already exists but is not sysbox-managed; refusing to destroy", spec.Name)
		}
		_, _ = exec.CommandContext(ctx, "virsh", "destroy", spec.Name).CombinedOutput()
	}
	if _, err := exec.CommandContext(ctx, "virsh", "dominfo", spec.Name).CombinedOutput(); err == nil {
		if !isSysboxManaged(ctx, spec.Name) {
			return substrate.NodeHandle{}, fmt.Errorf("libvirt: domain %q already defined but not sysbox-managed; refusing to undefine", spec.Name)
		}
		_, _ = exec.CommandContext(ctx, "virsh", "undefine", spec.Name).CombinedOutput()
	}

	vmDir, err := os.MkdirTemp("", "sysbox-lv-"+spec.Name+"-*")
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: create vm dir: %w", err)
	}
	if err := os.Chmod(vmDir, 0o755); err != nil {
		_ = os.RemoveAll(vmDir)
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: make vm dir accessible: %w", err)
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
	if err := os.Chmod(diskPath, 0o644); err != nil {
		_ = os.RemoveAll(vmDir)
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: make overlay accessible: %w", err)
	}

	machine := pc.MachineType
	if machine == "" {
		machine = "q35"
	}
	hs := &HandleState{
		DomainName:       spec.Name,
		DomainUUID:       uuid.NewString(),
		VMDir:            vmDir,
		DiskPath:         diskPath,
		VCPUs:            pc.VCPUs,
		MemoryMiB:        mem,
		MachineType:      machine,
		SSHUser:          pc.SSHUser,
		SSHPass:          pc.SSHPass,
		SSHKey:           pc.SSHKey,
		SSHAuthorizedKey: pc.SSHAuthorizedKey,
		NetworkInit:      pc.NetworkInit,
	}
	return substrate.NodeHandle{
		ID:       hs.DomainUUID,
		Provider: hs,
		Conn:     substrate.ConnInfo{Kind: substrate.ConnKindSSH},
	}, nil
}

func (s *Substrate) StartNode(ctx context.Context, h substrate.NodeHandle) error {
	hs := hsFrom(h)
	if hs.NetworkInit == substrate.GuestNetworkInitCloudInit && hs.SeedISO == "" {
		return fmt.Errorf("libvirt: cloud_init seed was not prepared")
	}
	if hs.DomainUUID != "" {
		if uuidOut, uuidErr := exec.CommandContext(ctx, "virsh", "domuuid", hs.DomainName).CombinedOutput(); uuidErr == nil {
			if strings.TrimSpace(string(uuidOut)) != hs.DomainUUID {
				return fmt.Errorf("libvirt: domain %q UUID ownership mismatch", hs.DomainName)
			}
			stateOut, stateErr := exec.CommandContext(ctx, "virsh", "domstate", hs.DomainName).CombinedOutput()
			if stateErr != nil {
				return fmt.Errorf("libvirt: read existing reset domain state: %w\n%s", stateErr, stateOut)
			}
			state := strings.TrimSpace(string(stateOut))
			if state == "running" || state == "paused" || state == "in shutdown" {
				return nil
			}
			if out, err := exec.CommandContext(ctx, "virsh", "start", hs.DomainName).CombinedOutput(); err != nil {
				return fmt.Errorf("libvirt: restart defined reset domain: %w\n%s", err, out)
			}
			setSysboxManaged(ctx, hs.DomainName)
			return nil
		}
	}

	xmlStr, err := GenerateDomainXML(DomainSpec{
		Name:        hs.DomainName,
		UUID:        hs.DomainUUID,
		VCPUs:       hs.VCPUs,
		MemoryMiB:   hs.MemoryMiB,
		MachineType: hs.MachineType,
		DiskPath:    hs.DiskPath,
		SeedISO:     hs.SeedISO,
		Bridges:     hs.Bridges,
	})
	if err != nil {
		return err
	}

	xmlPath := filepath.Join(hs.VMDir, "domain.xml")
	if err := os.WriteFile(xmlPath, []byte(xmlStr), 0o644); err != nil {
		return fmt.Errorf("libvirt: write domain xml: %w", err)
	}

	// Cleanup vmDir on any failure so disk images don't leak.
	if out, err := exec.CommandContext(ctx, "virsh", "define", xmlPath).CombinedOutput(); err != nil {
		if hs.DomainUUID == "" {
			_ = os.RemoveAll(hs.VMDir)
		}
		return fmt.Errorf("libvirt: virsh define: %w\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, "virsh", "start", hs.DomainName).CombinedOutput(); err != nil {
		if hs.DomainUUID == "" {
			_, _ = exec.Command("virsh", "undefine", hs.DomainName).CombinedOutput()
			_ = os.RemoveAll(hs.VMDir)
		}
		return fmt.Errorf("libvirt: virsh start: %w\n%s", err, out)
	}
	// Mark the domain as sysbox-managed so future CreateNode calls can
	// safely distinguish our domains from unrelated ones with the same name.
	setSysboxManaged(ctx, hs.DomainName)
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

func (s *Substrate) Pause(ctx context.Context, h substrate.NodeHandle) error {
	hs := hsFrom(h)
	if out, err := exec.CommandContext(ctx, "virsh", "suspend", hs.DomainName).CombinedOutput(); err != nil {
		return fmt.Errorf("libvirt: virsh suspend: %w\n%s", err, out)
	}
	return nil
}

func (s *Substrate) Resume(ctx context.Context, h substrate.NodeHandle) error {
	hs := hsFrom(h)
	if out, err := exec.CommandContext(ctx, "virsh", "resume", hs.DomainName).CombinedOutput(); err != nil {
		return fmt.Errorf("libvirt: virsh resume: %w\n%s", err, out)
	}
	return nil
}

// ReadNode queries libvirt for an existing domain by name and returns a
// NodeHandle suitable for importing into sysbox state.
func (s *Substrate) ReadNode(ctx context.Context, id string) (substrate.NodeHandle, error) {
	out, err := exec.CommandContext(ctx, "virsh", "dominfo", id).CombinedOutput()
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: domain %q not found: %w\n%s", id, err, out)
	}
	// Extract the domain UUID from dominfo output.
	var uuid string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "UUID:") {
			uuid = strings.TrimSpace(strings.TrimPrefix(line, "UUID:"))
		}
	}
	hs := &HandleState{DomainName: id}
	if uuid == "" {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: domain %q: could not extract UUID from dominfo", id)
	}
	return substrate.NodeHandle{
		ID:       uuid,
		Provider: hs,
		Conn:     substrate.ConnInfo{Kind: substrate.ConnKindSSH},
	}, nil
}

func (s *Substrate) NodeStatus(ctx context.Context, h substrate.NodeHandle) (bool, error) {
	hs := hsFrom(h)
	out, err := exec.CommandContext(ctx, "virsh", "domstate", hs.DomainName).CombinedOutput()
	if err != nil {
		return false, nil
	}
	state := strings.TrimSpace(string(out))
	// Both "running" and "paused" are healthy states; "paused" means the VM
	// exists and was explicitly suspended (sysbox pause), not crashed.
	return state == "running" || state == "paused" || state == "in shutdown", nil
}

func (s *Substrate) ObserveNode(ctx context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	hs := hsFrom(h)
	out, err := exec.CommandContext(ctx, "virsh", "domstate", hs.DomainName).CombinedOutput()
	if err != nil {
		reason := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(reason), "domain not found") || strings.Contains(strings.ToLower(reason), "failed to get domain") {
			return substrate.NodeObservation{Exists: false, Status: substrate.NodeStatusMissing, ExternalID: h.ID, Reason: reason, LastSeen: time.Now().UTC()}, nil
		}
		return substrate.NodeObservation{Status: substrate.NodeStatusUnknown, ExternalID: h.ID, Reason: reason, LastSeen: time.Now().UTC()}, fmt.Errorf("libvirt: observe domain state: %w", err)
	}
	domainState := strings.TrimSpace(string(out))
	status := substrate.NodeStatusExited
	running := domainState == "running" || domainState == "in shutdown"
	if running {
		status = substrate.NodeStatusRunning
	} else if domainState == "paused" {
		status = substrate.NodeStatusPaused
		running = true
	}
	externalID := h.ID
	if uuidOut, uuidErr := exec.CommandContext(ctx, "virsh", "domuuid", hs.DomainName).CombinedOutput(); uuidErr == nil && strings.TrimSpace(string(uuidOut)) != "" {
		externalID = strings.TrimSpace(string(uuidOut))
	} else if uuidErr != nil {
		return substrate.NodeObservation{Exists: true, Status: substrate.NodeStatusUnknown, ExternalID: h.ID, Reason: string(uuidOut), LastSeen: time.Now().UTC()}, fmt.Errorf("libvirt: observe domain UUID: %w", uuidErr)
	}
	if h.ID != "" && externalID != h.ID {
		return substrate.NodeObservation{Exists: true, Status: substrate.NodeStatusUnknown, ExternalID: externalID, Reason: "domain UUID mismatch", LastSeen: time.Now().UTC()}, fmt.Errorf("libvirt: domain UUID mismatch: have %s, want %s", externalID, h.ID)
	}
	return substrate.NodeObservation{Exists: true, Running: running, Healthy: running, Status: status, ExternalID: externalID, Reason: domainState, LastSeen: time.Now().UTC()}, nil
}

func (s *Substrate) PrepareHandle(_ context.Context, h *substrate.NodeHandle, providerConfig any, _ substrate.StateReader) error {
	hs := hsFrom(*h)
	if cfg, ok := providerConfig.(*Config); ok && cfg != nil {
		hs.SSHUser = cfg.SSHUser
		hs.SSHPass = cfg.SSHPass
		hs.SSHKey = cfg.SSHKey
		hs.SSHAuthorizedKey = cfg.SSHAuthorizedKey
	}
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

// isSysboxManaged checks whether a libvirt domain was created by sysbox
// by looking for the "sysbox-managed" title metadata.
func isSysboxManaged(ctx context.Context, name string) bool {
	out, err := exec.CommandContext(ctx, "virsh", "dominfo", name).CombinedOutput()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		// virsh dominfo shows "Title: sysbox-managed" if set.
		if strings.Contains(line, "sysbox-managed") {
			return true
		}
	}
	return false
}

// setSysboxManaged marks a domain as managed by sysbox via virsh metadata.
func setSysboxManaged(ctx context.Context, name string) {
	// Use virsh desc to set a title that identifies sysbox-managed domains.
	_, _ = exec.CommandContext(ctx, "virsh", "desc", name, "sysbox-managed", "--title").CombinedOutput()
}
