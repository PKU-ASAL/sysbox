package libvirt

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/substrate"
)

const libvirtResetHandleVersion = 2
const vmDirOwnershipFile = ".sysbox-owner.json"

type vmDirOwnership struct {
	DomainName string `json:"domain_name"`
	DomainUUID string `json:"domain_uuid"`
}

type resetHandleState struct {
	Identity       substrate.OperationIdentity `json:"identity"`
	OldIdentity    substrate.OperationIdentity `json:"old_identity"`
	Version        int                         `json:"version"`
	DomainName     string                      `json:"domain_name"`
	OldDomainUUID  string                      `json:"old_domain_uuid,omitempty"`
	OldVMDir       string                      `json:"old_vm_dir,omitempty"`
	NewDomainUUID  string                      `json:"new_domain_uuid"`
	NewVMDir       string                      `json:"new_vm_dir"`
	BaselinePath   string                      `json:"baseline_path"`
	BaselineDigest string                      `json:"baseline_digest"`
}

func (s *Substrate) PrepareReset(ctx context.Context, request substrate.ResetRequest) (substrate.ResetHandle, error) {
	if request.Baseline.Kind != substrate.ArtifactQCow2 || request.Node.Image.ID == "" || request.Node.Image.Identity.Digest != request.Baseline.Digest {
		return substrate.ResetHandle{}, fmt.Errorf("libvirt reset requires a pinned qcow2 baseline")
	}
	current := hsFrom(request.Current)
	if current.ExternalDefinition {
		return substrate.ResetHandle{}, fmt.Errorf("adopted external definition has no immutable reset baseline; explicit replacement required")
	}
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || identity.Validate() != nil || current.Identity == nil || current.Identity.Validate() != nil || request.Current.Generation != current.Identity.Generation || identity.Generation <= request.Current.Generation {
		return substrate.ResetHandle{}, fmt.Errorf("libvirt reset requires durable old and target generation identities")
	}
	old := identity
	old.Generation = request.Current.Generation
	if !sameGeneration(old, *current.Identity) || current.DomainUUID != request.Current.ID {
		return substrate.ResetHandle{}, fmt.Errorf("libvirt reset old generation ownership mismatch")
	}
	if current.DomainName == "" || current.DomainName != request.Node.Name {
		return substrate.ResetHandle{}, fmt.Errorf("libvirt reset domain identity mismatch: current=%q requested=%q", current.DomainName, request.Node.Name)
	}
	if err := validateOwnedVMDirInRoot(s.vmDirRoot(), current.DomainName, request.Current.ID, current.VMDir, current.DiskPath); err != nil {
		return substrate.ResetHandle{}, err
	}
	newDomainUUID := uuid.NewString()
	newVMDir, err := s.managedVMDir(identity, request.Node.Name)
	if err != nil {
		return substrate.ResetHandle{}, err
	}
	state := &resetHandleState{
		Identity: identity, OldIdentity: *current.Identity,
		Version: libvirtResetHandleVersion, DomainName: request.Node.Name,
		OldDomainUUID: request.Current.ID, OldVMDir: current.VMDir,
		NewDomainUUID: newDomainUUID, NewVMDir: newVMDir,
		BaselinePath: request.Node.Image.ID, BaselineDigest: request.Baseline.Digest,
	}
	if err := s.validateResetRecord(state, true, false); err != nil {
		return substrate.ResetHandle{}, err
	}
	return substrate.ResetHandle{Provider: state, Request: request}, nil
}

func (s *Substrate) DestroyReset(ctx context.Context, handle substrate.ResetHandle) error {
	state, _, err := libvirtResetState(handle)
	if err != nil {
		return err
	}
	if err := validateLibvirtResetIdentity(ctx, state); err != nil {
		return err
	}
	if err := s.validateResetPaths(state); err != nil {
		return err
	}
	if _, err := os.Lstat(state.OldVMDir); err == nil {
		if err := s.validateResetRecord(state, true, false); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := s.verifyResetDomain(ctx, state, true); err != nil {
		return err
	}
	hs := &HandleState{Identity: &state.OldIdentity, DomainName: state.DomainName, DomainUUID: state.OldDomainUUID, VMDir: state.OldVMDir, DiskPath: filepath.Join(state.OldVMDir, "disk.qcow2")}
	// The validated reset authorizes removal of its recorded old generation.
	return s.DestroyNode(substrate.WithOperationIdentity(ctx, state.OldIdentity), managedNodeHandle(hs))
}

func domainOwnsDisk(ctx context.Context, domainName, expectedDisk string) (bool, error) {
	out, err := exec.CommandContext(ctx, "virsh", "dumpxml", domainName).CombinedOutput()
	if err != nil {
		return false, err
	}
	var domain domainXML
	if err := xml.Unmarshal(out, &domain); err != nil {
		return false, err
	}
	for _, disk := range domain.Devices.Disks {
		if disk.Device == "disk" && filepath.Clean(disk.Source.File) == filepath.Clean(expectedDisk) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Substrate) ApplyReset(ctx context.Context, handle substrate.ResetHandle) (substrate.NodeHandle, error) {
	state, request, err := libvirtResetState(handle)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := validateLibvirtResetIdentity(ctx, state); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := s.validateResetPaths(state); err != nil {
		return substrate.NodeHandle{}, err
	}
	exists, err := s.verifyResetDomain(ctx, state, false)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if exists {
		recovered, err := s.DiscoverManagedNode(ctx, state.Identity, state.DomainName)
		if err != nil {
			return substrate.NodeHandle{}, err
		}
		if recovered.ID != state.NewDomainUUID {
			return substrate.NodeHandle{}, fmt.Errorf("libvirt reset replacement exact UUID mismatch")
		}
		return recovered, nil
	}
	if err := domainNameAvailable(ctx, state.DomainName); err != nil {
		return substrate.NodeHandle{}, err
	}
	resolved, err := artifact.NewWithContext(ctx).ResolveIdentity(artifact.IdentitySpec{Kind: substrate.ArtifactQCow2, Source: state.BaselinePath, ExpectedDigest: state.BaselineDigest, Architecture: request.Baseline.Architecture, GuestFamily: request.Baseline.GuestFamily})
	if err != nil || resolved.Identity.Digest != state.BaselineDigest {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt reset baseline changed: %w", err)
	}
	diskPath := filepath.Join(state.NewVMDir, "disk.qcow2")
	stagedBase := filepath.Join(state.NewVMDir, "base.qcow2")
	if err := os.MkdirAll(state.NewVMDir, 0o755); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := ensureVMDirOwnership(state.NewVMDir, state.DomainName, state.NewDomainUUID); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := s.validateResetRecord(state, false, true); err != nil {
		return substrate.NodeHandle{}, err
	}
	hs, err := resetLibvirtHandleState(request, state, diskPath)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := persistManagedRecord(hs, false); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := stageBaseImage(ctx, state.BaselinePath, stagedBase); err != nil {
		return substrate.NodeHandle{}, err
	}
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		args := []string{"create", "-f", "qcow2", "-b", stagedBase, "-F", "qcow2", diskPath}
		if cfg, ok := request.Node.ProviderConfig.(*Config); ok && cfg != nil && cfg.DiskSize != "" {
			args = append(args, cfg.DiskSize)
		}
		if out, createErr := exec.CommandContext(ctx, "qemu-img", args...).CombinedOutput(); createErr != nil {
			return substrate.NodeHandle{}, fmt.Errorf("libvirt reset create overlay: %w\n%s", createErr, out)
		}
		if err := os.Chmod(diskPath, 0o644); err != nil {
			return substrate.NodeHandle{}, err
		}
	} else if err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := validateResetOverlay(ctx, diskPath, stagedBase); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := ensureCloudInitSSHAccess(ctx, hs); err != nil {
		return substrate.NodeHandle{}, err
	}
	if err := persistManagedRecord(hs, true); err != nil {
		return substrate.NodeHandle{}, err
	}
	return managedNodeHandle(hs), nil
}

func validateResetOverlay(ctx context.Context, diskPath, baselinePath string) error {
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--force-share", "--output=json", diskPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("libvirt reset inspect overlay: %w\n%s", err, out)
	}
	var info struct {
		BackingFilename string `json:"backing-filename"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return fmt.Errorf("libvirt reset decode overlay info: %w", err)
	}
	if filepath.Clean(info.BackingFilename) != filepath.Clean(baselinePath) {
		return fmt.Errorf("libvirt reset overlay backing %q does not match baseline %q", info.BackingFilename, baselinePath)
	}
	return nil
}

func (s *Substrate) ObserveReset(ctx context.Context, handle substrate.ResetHandle) (substrate.ResetObservation, error) {
	state, _, err := libvirtResetState(handle)
	if err != nil {
		return substrate.ResetObservation{}, err
	}
	if err := validateLibvirtResetIdentity(ctx, state); err != nil {
		return substrate.ResetObservation{}, err
	}
	if err := s.validateResetPaths(state); err != nil {
		return substrate.ResetObservation{}, err
	}
	observation := substrate.ResetObservation{Phase: substrate.ResetPhaseApplying, OldExternalID: state.OldDomainUUID, NewExternalID: state.NewDomainUUID, BaselineDigest: state.BaselineDigest}
	oldExists, err := s.verifyResetDomain(ctx, state, true)
	if err != nil {
		return observation, err
	}
	if oldExists {
		observation.Residue = append(observation.Residue, state.OldDomainUUID)
	}
	if state.OldVMDir != "" {
		if _, err := os.Stat(state.OldVMDir); err == nil {
			observation.Residue = append(observation.Residue, state.OldVMDir)
		} else if !os.IsNotExist(err) {
			return observation, err
		}
	}
	if _, err := os.Lstat(state.NewVMDir); os.IsNotExist(err) {
		observation.Reason = "replacement generation not created"
		return observation, nil
	} else if err != nil {
		return observation, err
	}
	if err := s.validateResetRecord(state, false, false); err != nil {
		return observation, err
	}
	diskPath := filepath.Join(state.NewVMDir, "disk.qcow2")
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--force-share", "--output=json", diskPath).CombinedOutput()
	if err != nil {
		observation.Reason = "replacement overlay is unavailable"
		return observation, nil
	}
	var info struct {
		BackingFilename string `json:"backing-filename"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return observation, fmt.Errorf("libvirt reset decode overlay info: %w", err)
	}
	if filepath.Clean(info.BackingFilename) != filepath.Join(filepath.Clean(state.NewVMDir), "base.qcow2") {
		observation.Reason = "replacement overlay backing file does not match baseline"
		return observation, nil
	}
	exists, err := s.verifyResetDomain(ctx, state, false)
	if err != nil {
		return observation, err
	}
	if !exists {
		observation.Reason = "replacement domain UUID does not match reset handle"
		return observation, nil
	}
	if len(observation.Residue) == 0 {
		observation.Phase = substrate.ResetPhaseComplete
		observation.Converged = true
	}
	return observation, nil
}

func (s *Substrate) CleanupReset(ctx context.Context, handle substrate.ResetHandle) error {
	return s.DestroyReset(ctx, handle)
}

func (s *Substrate) MarshalResetHandle(handle substrate.ResetHandle) (json.RawMessage, error) {
	state, ok := handle.Provider.(*resetHandleState)
	if !ok || state == nil {
		return nil, fmt.Errorf("libvirt reset handle has type %T", handle.Provider)
	}
	return json.Marshal(state)
}

func (s *Substrate) UnmarshalResetHandle(raw json.RawMessage) (substrate.ResetHandle, error) {
	var state resetHandleState
	if err := json.Unmarshal(raw, &state); err != nil {
		return substrate.ResetHandle{}, err
	}
	if state.Version != libvirtResetHandleVersion {
		return substrate.ResetHandle{}, fmt.Errorf("libvirt reset handle version %d is unsupported", state.Version)
	}
	return substrate.ResetHandle{Provider: &state}, nil
}

func libvirtResetState(handle substrate.ResetHandle) (*resetHandleState, substrate.ResetRequest, error) {
	state, ok := handle.Provider.(*resetHandleState)
	if !ok || state == nil || state.Version != libvirtResetHandleVersion {
		return nil, substrate.ResetRequest{}, fmt.Errorf("invalid libvirt reset handle")
	}
	if state.Identity.Validate() != nil || state.OldIdentity.Validate() != nil || state.Identity.Generation <= state.OldIdentity.Generation || state.NewDomainUUID == "" || state.OldDomainUUID == "" {
		return nil, substrate.ResetRequest{}, fmt.Errorf("libvirt reset handle lacks generation identities")
	}
	if handle.Request.Node.Name == "" {
		return nil, substrate.ResetRequest{}, fmt.Errorf("libvirt reset request was not injected by runtime")
	}
	return state, handle.Request, nil
}

func resetLibvirtHandleState(request substrate.ResetRequest, state *resetHandleState, diskPath string) (*HandleState, error) {
	cfg, _ := request.Node.ProviderConfig.(*Config)
	if cfg == nil {
		return nil, fmt.Errorf("libvirt reset requires provider config")
	}
	memory, err := parseMiB(cfg.Memory)
	if err != nil {
		return nil, err
	}
	machine := cfg.MachineType
	if machine == "" {
		machine = "q35"
	}
	return &HandleState{
		GuestFamily: request.Node.Image.Identity.GuestFamily,
		Identity:    &state.Identity,
		DomainName:  state.DomainName, DomainUUID: state.NewDomainUUID, VMDir: state.NewVMDir, DiskPath: diskPath,
		VCPUs: cfg.VCPUs, MemoryMiB: memory, MachineType: machine,
		SSHUser: cfg.SSHUser, SSHPass: cfg.SSHPass, SSHKey: cfg.SSHKey, SSHAuthorizedKey: cfg.SSHAuthorizedKey,
		NetworkInit: cfg.NetworkInit,
	}, nil
}

func ensureVMDirOwnership(vmDir, domainName, domainUUID string) error {
	info, err := os.Lstat(vmDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("libvirt refuses symlinked or non-directory VM ownership")
	}
	path := filepath.Join(vmDir, vmDirOwnershipFile)
	if raw, err := os.ReadFile(path); err == nil {
		var ownership vmDirOwnership
		if json.Unmarshal(raw, &ownership) == nil && ownership.DomainName == domainName && ownership.DomainUUID == domainUUID {
			return nil
		}
		return fmt.Errorf("libvirt reset VM directory ownership mismatch")
	} else if !os.IsNotExist(err) {
		return err
	}
	entries, err := os.ReadDir(vmDir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("libvirt reset refuses VM directory without ownership manifest")
	}
	raw, err := json.Marshal(vmDirOwnership{DomainName: domainName, DomainUUID: domainUUID})
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func validateOwnedVMDir(domainName, domainUUID, vmDir, diskPath string) error {
	return validateOwnedVMDirInRoot(os.TempDir(), domainName, domainUUID, vmDir, diskPath)
}

func validateOwnedVMDirInRoot(root, domainName, domainUUID, vmDir, diskPath string) error {
	if vmDir == "" {
		return nil
	}
	cleanDir := filepath.Clean(vmDir)
	prefix := "sysbox-lv-" + domainName + "-"
	if filepath.Dir(cleanDir) != filepath.Clean(root) || !strings.HasPrefix(filepath.Base(cleanDir), prefix) {
		return fmt.Errorf("libvirt reset refuses unowned VM directory %q", vmDir)
	}
	if diskPath != "" && filepath.Dir(filepath.Clean(diskPath)) != cleanDir {
		return fmt.Errorf("libvirt reset disk %q is outside owned VM directory", diskPath)
	}
	info, err := os.Lstat(vmDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("libvirt refuses symlinked or non-directory VM ownership")
	}
	raw, err := os.ReadFile(filepath.Join(cleanDir, vmDirOwnershipFile))
	if err != nil {
		return fmt.Errorf("libvirt reset VM directory ownership manifest: %w", err)
	}
	var ownership vmDirOwnership
	if err := json.Unmarshal(raw, &ownership); err != nil || ownership.DomainName != domainName || ownership.DomainUUID != domainUUID {
		return fmt.Errorf("libvirt reset VM directory ownership mismatch")
	}
	return nil
}
