package libvirt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/substrate"
)

const libvirtResetHandleVersion = 1

type resetHandleState struct {
	Version        int    `json:"version"`
	DomainName     string `json:"domain_name"`
	OldDomainUUID  string `json:"old_domain_uuid,omitempty"`
	OldVMDir       string `json:"old_vm_dir,omitempty"`
	NewDomainUUID  string `json:"new_domain_uuid"`
	NewVMDir       string `json:"new_vm_dir"`
	BaselinePath   string `json:"baseline_path"`
	BaselineDigest string `json:"baseline_digest"`
}

func (s *Substrate) PrepareReset(ctx context.Context, request substrate.ResetRequest) (substrate.ResetHandle, error) {
	if request.Baseline.Kind != substrate.ArtifactQCow2 || request.Node.Image.ID == "" || request.Node.Image.Identity.Digest != request.Baseline.Digest {
		return substrate.ResetHandle{}, fmt.Errorf("libvirt reset requires a pinned qcow2 baseline")
	}
	current := hsFrom(request.Current)
	if current.DomainName == "" || current.DomainName != request.Node.Name {
		return substrate.ResetHandle{}, fmt.Errorf("libvirt reset domain identity mismatch")
	}
	if err := validateOwnedVMDir(current.DomainName, current.VMDir, current.DiskPath); err != nil {
		return substrate.ResetHandle{}, err
	}
	newDomainUUID := uuid.NewString()
	newVMDir := filepath.Join(os.TempDir(), "sysbox-lv-"+request.Node.Name+"-reset-"+newDomainUUID)
	state := &resetHandleState{
		Version: libvirtResetHandleVersion, DomainName: request.Node.Name,
		OldDomainUUID: request.Current.ID, OldVMDir: current.VMDir,
		NewDomainUUID: newDomainUUID, NewVMDir: newVMDir,
		BaselinePath: request.Node.Image.ID, BaselineDigest: request.Baseline.Digest,
	}
	return substrate.ResetHandle{Provider: state, Request: request}, nil
}

func (s *Substrate) DestroyReset(ctx context.Context, handle substrate.ResetHandle) error {
	state, _, err := libvirtResetState(handle)
	if err != nil {
		return err
	}
	if out, infoErr := exec.CommandContext(ctx, "virsh", "dominfo", state.DomainName).CombinedOutput(); infoErr == nil {
		if !strings.Contains(string(out), "sysbox-managed") {
			return fmt.Errorf("libvirt reset refuses unmanaged domain %q", state.DomainName)
		}
		uuidOut, uuidErr := exec.CommandContext(ctx, "virsh", "domuuid", state.DomainName).CombinedOutput()
		if uuidErr != nil || strings.TrimSpace(string(uuidOut)) != state.OldDomainUUID {
			return fmt.Errorf("libvirt reset domain UUID ownership mismatch for %q", state.DomainName)
		}
		_, _ = exec.CommandContext(ctx, "virsh", "destroy", state.DomainName).CombinedOutput()
		if undefineOut, undefineErr := exec.CommandContext(ctx, "virsh", "undefine", state.DomainName).CombinedOutput(); undefineErr != nil {
			return fmt.Errorf("libvirt reset undefine old domain: %w\n%s", undefineErr, undefineOut)
		}
	}
	if state.OldVMDir != "" {
		return os.RemoveAll(state.OldVMDir)
	}
	return nil
}

func (s *Substrate) ApplyReset(ctx context.Context, handle substrate.ResetHandle) (substrate.NodeHandle, error) {
	state, request, err := libvirtResetState(handle)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	resolved, err := artifact.New().ResolveIdentity(artifact.IdentitySpec{Kind: substrate.ArtifactQCow2, Source: state.BaselinePath, ExpectedDigest: state.BaselineDigest, Architecture: request.Baseline.Architecture, GuestFamily: request.Baseline.GuestFamily})
	if err != nil || resolved.Identity.Digest != state.BaselineDigest {
		return substrate.NodeHandle{}, fmt.Errorf("libvirt reset baseline changed: %w", err)
	}
	diskPath := filepath.Join(state.NewVMDir, "disk.qcow2")
	if err := os.MkdirAll(state.NewVMDir, 0o755); err != nil {
		return substrate.NodeHandle{}, err
	}
	if _, err := os.Stat(diskPath); os.IsNotExist(err) {
		args := []string{"create", "-f", "qcow2", "-b", state.BaselinePath, "-F", "qcow2", diskPath}
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
	if err := validateResetOverlay(ctx, diskPath, state.BaselinePath); err != nil {
		return substrate.NodeHandle{}, err
	}
	hs, err := resetLibvirtHandleState(request, state, diskPath)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	return substrate.NodeHandle{ID: state.NewDomainUUID, Provider: hs, Conn: substrate.ConnInfo{Kind: substrate.ConnKindSSH}}, nil
}

func validateResetOverlay(ctx context.Context, diskPath, baselinePath string) error {
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", diskPath).CombinedOutput()
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
	observation := substrate.ResetObservation{Phase: substrate.ResetPhaseApplying, OldExternalID: state.OldDomainUUID, NewExternalID: state.NewDomainUUID, BaselineDigest: state.BaselineDigest}
	if state.OldVMDir != "" {
		if _, err := os.Stat(state.OldVMDir); err == nil {
			observation.Residue = append(observation.Residue, state.OldVMDir)
		}
	}
	diskPath := filepath.Join(state.NewVMDir, "disk.qcow2")
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", diskPath).CombinedOutput()
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
	if filepath.Clean(info.BackingFilename) != filepath.Clean(state.BaselinePath) {
		observation.Reason = "replacement overlay backing file does not match baseline"
		return observation, nil
	}
	uuidOut, err := exec.CommandContext(ctx, "virsh", "domuuid", state.DomainName).CombinedOutput()
	if err != nil || strings.TrimSpace(string(uuidOut)) != state.NewDomainUUID {
		observation.Reason = "replacement domain UUID does not match reset handle"
		return observation, nil
	}
	if len(observation.Residue) == 0 {
		observation.Phase = substrate.ResetPhaseComplete
		observation.Converged = true
	}
	return observation, nil
}

func (s *Substrate) CleanupReset(_ context.Context, handle substrate.ResetHandle) error {
	state, _, err := libvirtResetState(handle)
	if err != nil {
		return err
	}
	if state.OldVMDir == "" {
		return nil
	}
	if err := validateOwnedVMDir(state.DomainName, state.OldVMDir, filepath.Join(state.OldVMDir, "disk.qcow2")); err != nil {
		return err
	}
	return os.RemoveAll(state.OldVMDir)
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
		DomainName: state.DomainName, DomainUUID: state.NewDomainUUID, VMDir: state.NewVMDir, DiskPath: diskPath,
		VCPUs: cfg.VCPUs, MemoryMiB: memory, MachineType: machine,
		SSHUser: cfg.SSHUser, SSHPass: cfg.SSHPass, SSHKey: cfg.SSHKey, SSHAuthorizedKey: cfg.SSHAuthorizedKey,
		NetworkInit: cfg.NetworkInit,
	}, nil
}

func validateOwnedVMDir(domainName, vmDir, diskPath string) error {
	if vmDir == "" {
		return nil
	}
	cleanDir := filepath.Clean(vmDir)
	prefix := "sysbox-lv-" + domainName + "-"
	if filepath.Dir(cleanDir) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(cleanDir), prefix) {
		return fmt.Errorf("libvirt reset refuses unowned VM directory %q", vmDir)
	}
	if diskPath != "" && filepath.Dir(filepath.Clean(diskPath)) != cleanDir {
		return fmt.Errorf("libvirt reset disk %q is outside owned VM directory", diskPath)
	}
	return nil
}
