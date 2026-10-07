package libvirt

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// HandleState is the libvirt substrate's typed NodeHandle.Provider payload.
type HandleState struct {
	Identity         *substrate.OperationIdentity   `json:"identity,omitempty"`
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

	identity, managed := substrate.OperationIdentityFromContext(ctx)
	if !managed || identity.Validate() != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, subName, "create requires durable operation identity", nil)
	}
	existing, err := s.DiscoverManagedNode(ctx, identity, spec.Name)
	if err == nil {
		return existing, nil
	}
	if !driver.IsCategory(err, driver.ErrorNotFound) {
		return substrate.NodeHandle{}, err
	}

	if err := os.MkdirAll(s.vmDirRoot(), 0o755); err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: create workdir: %w", err)
	}
	vmDir, err := s.managedVMDir(identity, spec.Name)
	if err == nil {
		err = os.Mkdir(vmDir, 0o755)
	}
	if err != nil {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: create vm dir: %w", err)
	}
	if err := os.Chmod(vmDir, 0o755); err != nil {
		_ = os.RemoveAll(vmDir)
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: make vm dir accessible: %w", err)
	}
	domainUUID := uuid.NewString()
	if err := ensureVMDirOwnership(vmDir, spec.Name, domainUUID); err != nil {
		_ = os.RemoveAll(vmDir)
		return substrate.NodeHandle{}, fmt.Errorf("libvirt: record vm dir ownership: %w", err)
	}

	diskPath := filepath.Join(vmDir, "disk.qcow2")
	machine := pc.MachineType
	if machine == "" {
		machine = "q35"
	}
	hs := &HandleState{DomainName: spec.Name, DomainUUID: domainUUID, VMDir: vmDir, DiskPath: diskPath, VCPUs: pc.VCPUs, MemoryMiB: mem, MachineType: machine, SSHUser: pc.SSHUser, SSHPass: pc.SSHPass, SSHKey: pc.SSHKey, SSHAuthorizedKey: pc.SSHAuthorizedKey, NetworkInit: pc.NetworkInit}
	hs.Identity = &identity
	if err := persistManagedRecord(hs, false); err != nil {
		return substrate.NodeHandle{}, err
	}
	stagedBase := filepath.Join(vmDir, "base.qcow2")
	if err := stageBaseImage(ctx, baseImage, stagedBase); err != nil {
		_ = os.RemoveAll(vmDir)
		return substrate.NodeHandle{}, err
	}
	qiArgs := []string{"create", "-f", "qcow2", "-b", stagedBase, "-F", "qcow2", diskPath}
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

	if err := ensureCloudInitSSHAccess(ctx, hs); err != nil {
		_ = os.RemoveAll(vmDir)
		return substrate.NodeHandle{}, err
	}
	if err := persistManagedRecord(hs, true); err != nil {
		return substrate.NodeHandle{}, err
	}
	return managedNodeHandle(hs), nil
}

func ensureCloudInitSSHAccess(ctx context.Context, hs *HandleState) error {
	if hs == nil || hs.NetworkInit != substrate.GuestNetworkInitCloudInit || hs.SSHAuthorizedKey != "" {
		return nil
	}
	if hs.SSHKey != "" {
		out, err := exec.CommandContext(ctx, "ssh-keygen", "-y", "-f", hs.SSHKey).CombinedOutput()
		if err != nil {
			return fmt.Errorf("libvirt: derive SSH public key: %w\n%s", err, out)
		}
		hs.SSHAuthorizedKey = strings.TrimSpace(string(out))
		return nil
	}
	privateKey := filepath.Join(hs.VMDir, "guest-ssh-key")
	out, err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "sysbox@"+hs.DomainName, "-f", privateKey).CombinedOutput()
	if err != nil {
		return fmt.Errorf("libvirt: generate ephemeral SSH key: %w\n%s", err, out)
	}
	if err := os.Chmod(privateKey, 0o600); err != nil {
		return fmt.Errorf("libvirt: protect ephemeral SSH key: %w", err)
	}
	publicKey, err := os.ReadFile(privateKey + ".pub")
	if err != nil {
		return fmt.Errorf("libvirt: read ephemeral SSH public key: %w", err)
	}
	hs.SSHKey = privateKey
	hs.SSHAuthorizedKey = strings.TrimSpace(string(publicKey))
	return nil
}

func stageBaseImage(ctx context.Context, source, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("libvirt: inspect staged base image: %w", err)
	}
	temporary := destination + ".tmp"
	_ = os.Remove(temporary)
	out, err := exec.CommandContext(ctx, "qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", source, temporary).CombinedOutput()
	if err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("libvirt: stage base image: %w\n%s", err, out)
	}
	if err := os.Chmod(temporary, 0o644); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("libvirt: make staged base accessible: %w", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("libvirt: publish staged base image: %w", err)
	}
	return nil
}

func (s *Substrate) StartNode(ctx context.Context, h substrate.NodeHandle) error {
	if err := validateHandleIdentity(ctx, h); err != nil {
		return err
	}
	hs := hsFrom(h)
	if len(hs.Bridges) > 0 {
		targets := make([]network.BridgeTarget, 0, len(hs.Bridges))
		for _, bridge := range hs.Bridges {
			if bridge.Target.Bridge != bridge.Bridge || !bridge.Target.Root {
				return driver.Wrap(driver.ErrorInvalidState, subName, "network target proof missing or conflicting; rebuild VM", nil)
			}
			targets = append(targets, bridge.Target)
		}
		_, unlock, err := network.LockBridgeTargets(ctx, targets)
		if err != nil {
			return err
		}
		defer unlock()
	}
	if hs.Identity != nil {
		if identity, ok := substrate.OperationIdentityFromContext(ctx); ok && !sameGeneration(identity, *hs.Identity) {
			return driver.Wrap(driver.ErrorInvalidState, subName, "operation generation does not own domain", nil)
		}
		discovered, err := s.DiscoverManagedNode(ctx, *hs.Identity, hs.DomainName)
		if err != nil {
			return err
		}
		if discovered.ID != hs.DomainUUID {
			return driver.Wrap(driver.ErrorInvalidState, subName, "domain UUID ownership mismatch", nil)
		}
	}
	if hs.NetworkInit == substrate.GuestNetworkInitCloudInit && hs.SeedISO == "" {
		return fmt.Errorf("libvirt: cloud_init seed was not prepared")
	}
	names, err := domainNames(ctx)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == hs.DomainName {
			uuidOut, uuidErr := virshQuery(ctx, "domuuid", hs.DomainName)
			if uuidErr != nil {
				return uuidErr
			}
			if strings.TrimSpace(string(uuidOut)) != hs.DomainUUID {
				return fmt.Errorf("libvirt: domain %q UUID ownership mismatch", hs.DomainName)
			}
			xml, err := virshQuery(ctx, "dumpxml", hs.DomainUUID, "--inactive")
			if err != nil {
				return err
			}
			if err := validateDomainInterfaces(xml, hs.Bridges); err != nil {
				return err
			}
			stateOut, stateErr := exec.CommandContext(ctx, "virsh", "domstate", hs.DomainName).CombinedOutput()
			if stateErr != nil {
				return fmt.Errorf("libvirt: read existing reset domain state: %w\n%s", stateErr, stateOut)
			}
			state := strings.TrimSpace(string(stateOut))
			if state == "running" || state == "paused" || state == "in shutdown" {
				return nil
			}
			if out, err := exec.CommandContext(ctx, "virsh", "start", hs.DomainUUID).CombinedOutput(); err != nil {
				return fmt.Errorf("libvirt: restart defined reset domain: %w\n%s", err, out)
			}
			setSysboxManaged(ctx, hs.DomainName)
			return nil
		}
	}

	xmlStr, err := GenerateDomainXML(DomainSpec{
		Identity: hs.Identity, ManagedState: hs,
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
	if err := persistManagedRecord(hs, true); err != nil {
		return err
	}
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
	if out, err := exec.CommandContext(ctx, "virsh", "start", hs.DomainUUID).CombinedOutput(); err != nil {
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
	target, exists, err := existingDomainUUID(ctx, h)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if _, err := exec.CommandContext(ctx, "virsh", "shutdown", target).CombinedOutput(); err != nil {
		if _, err := exec.CommandContext(ctx, "virsh", "destroy", target).CombinedOutput(); err != nil {
			return driver.Wrap(driver.ErrorUnavailable, subName, "stop domain failed", err)
		}
	}
	return nil
}

func (s *Substrate) DestroyNode(ctx context.Context, h substrate.NodeHandle) error {
	hs := hsFrom(h)
	target, exists, err := existingDomainUUID(ctx, h)
	if err != nil {
		return err
	}
	if exists {
		state, err := virshQuery(ctx, "domstate", target)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(state)) != "shut off" {
			if _, err := exec.CommandContext(ctx, "virsh", "destroy", target).CombinedOutput(); err != nil {
				return driver.Wrap(driver.ErrorUnavailable, subName, "destroy domain failed", err)
			}
		}
		if _, err := exec.CommandContext(ctx, "virsh", "undefine", target).CombinedOutput(); err != nil {
			return driver.Wrap(driver.ErrorUnavailable, subName, "undefine domain failed", err)
		}
	}
	if hs.VMDir != "" {
		if _, err := os.Lstat(hs.VMDir); os.IsNotExist(err) {
			return nil
		}
		if err := validateOwnedVMDirInRoot(s.vmDirRoot(), hs.DomainName, hs.DomainUUID, hs.VMDir, hs.DiskPath); err != nil {
			return err
		}
		return os.RemoveAll(hs.VMDir)
	}
	return nil
}

func (s *Substrate) Pause(ctx context.Context, h substrate.NodeHandle) error {
	target, exists, err := existingDomainUUID(ctx, h)
	if err != nil {
		return err
	}
	if !exists {
		return driver.Wrap(driver.ErrorNotFound, subName, "domain absent", nil)
	}
	if out, err := exec.CommandContext(ctx, "virsh", "suspend", target).CombinedOutput(); err != nil {
		return fmt.Errorf("libvirt: virsh suspend: %w\n%s", err, out)
	}
	return nil
}

func (s *Substrate) Resume(ctx context.Context, h substrate.NodeHandle) error {
	target, exists, err := existingDomainUUID(ctx, h)
	if err != nil {
		return err
	}
	if !exists {
		return driver.Wrap(driver.ErrorNotFound, subName, "domain absent", nil)
	}
	if out, err := exec.CommandContext(ctx, "virsh", "resume", target).CombinedOutput(); err != nil {
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
	target, exists, err := existingDomainUUID(ctx, h)
	if err != nil {
		return substrate.NodeObservation{Status: substrate.NodeStatusUnknown, ExternalID: h.ID, LastSeen: time.Now().UTC()}, err
	}
	if !exists {
		return substrate.NodeObservation{Status: substrate.NodeStatusMissing, ExternalID: target, LastSeen: time.Now().UTC()}, nil
	}
	raw, err := virshQuery(ctx, "dumpxml", target)
	if err != nil {
		return substrate.NodeObservation{Status: substrate.NodeStatusUnknown, ExternalID: target}, err
	}
	var domain domainXML
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return substrate.NodeObservation{}, err
	}
	if domain.UUID != target || domain.Metadata == nil || domain.Metadata.Managed == nil {
		return substrate.NodeObservation{}, fmt.Errorf("libvirt: observed domain ownership metadata missing")
	}
	m := domain.Metadata.Managed
	actual := substrate.OperationIdentity{ActionID: m.ActionID, OwnerUID: m.OwnerUID, TopologyUID: m.TopologyUID, ResourceUID: m.ResourceUID, Generation: m.Generation}
	if actual.Validate() != nil || !sameGeneration(actual, *hsFrom(h).Identity) {
		return substrate.NodeObservation{}, fmt.Errorf("libvirt: observed domain ownership or generation conflict")
	}
	out, err := virshQuery(ctx, "domstate", target)
	if err != nil {
		reason := strings.TrimSpace(string(out))
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
	return substrate.NodeObservation{Exists: true, Running: running, Healthy: running, Status: status, ExternalID: target, OwnerUID: actual.OwnerUID, ResourceUID: actual.ResourceUID, Generation: actual.Generation, Reason: domainState, LastSeen: time.Now().UTC()}, nil
}

func (s *Substrate) PrepareHandle(_ context.Context, h *substrate.NodeHandle, providerConfig any, _ substrate.StateReader) error {
	hs := hsFrom(*h)
	if cfg, ok := providerConfig.(*Config); ok && cfg != nil {
		if cfg.SSHUser != "" {
			hs.SSHUser = cfg.SSHUser
		}
		if cfg.SSHPass != "" {
			hs.SSHPass = cfg.SSHPass
		}
		if cfg.SSHKey != "" {
			hs.SSHKey = cfg.SSHKey
		}
		if cfg.SSHAuthorizedKey != "" {
			hs.SSHAuthorizedKey = cfg.SSHAuthorizedKey
		}
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
