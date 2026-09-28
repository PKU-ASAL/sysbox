package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

const firecrackerResetHandleVersion = 2

type resetHandleState struct {
	OldProcess     processIdentity             `json:"old_process"`
	NewIdentity    substrate.OperationIdentity `json:"new_identity"`
	Version        int                         `json:"version"`
	OldID          string                      `json:"old_id"`
	OldVMDir       string                      `json:"old_vm_dir"`
	OldPID         int                         `json:"old_pid,omitempty"`
	OldPIDStart    string                      `json:"old_pid_start,omitempty"`
	OldSocket      string                      `json:"old_socket,omitempty"`
	NewID          string                      `json:"new_id"`
	BaselinePath   string                      `json:"baseline_path"`
	BaselineDigest string                      `json:"baseline_digest"`
	NewHandle      *HandleState                `json:"new_handle,omitempty"`
}

func (s *Substrate) PrepareReset(ctx context.Context, request substrate.ResetRequest) (substrate.ResetHandle, error) {
	if request.Baseline.Kind != substrate.ArtifactRootFS || request.Node.Image.ID == "" || request.Node.Image.Identity.Digest != request.Baseline.Digest {
		return substrate.ResetHandle{}, fmt.Errorf("firecracker reset requires a pinned rootfs baseline")
	}
	if cfg, ok := request.Node.ProviderConfig.(*Config); ok && cfg != nil && cfg.Rootfs != "" && filepath.Clean(cfg.Rootfs) != filepath.Clean(request.Node.Image.ID) {
		return substrate.ResetHandle{}, fmt.Errorf("firecracker reset rootfs override does not match pinned baseline")
	}
	target, supplied := substrate.OperationIdentityFromContext(ctx)
	oldCtx := ctx
	if supplied {
		current, _ := request.Current.Provider.(*HandleState)
		if err := validateResetTarget(target, current); err != nil {
			return substrate.ResetHandle{}, err
		}
		old := target
		old.Generation = current.Generation
		oldCtx = substrate.WithOperationIdentity(ctx, old)
	}
	_, m, release, err := s.lockedManifest(oldCtx, request.Current)
	if err != nil {
		return substrate.ResetHandle{}, err
	}
	defer release()
	if err := recoverLaunch(m); err != nil {
		return substrate.ResetHandle{}, err
	}
	if _, _, err := probeProcess(m.Process); err != nil {
		return substrate.ResetHandle{}, err
	}
	generation := uuid.NewString()
	if !supplied {
		target = substrate.OperationIdentity{OwnerUID: m.OwnerUID, ResourceUID: m.ResourceUID, TopologyUID: m.TopologyUID, Generation: m.Generation + 1, ActionID: generation}
	}
	state := &resetHandleState{Version: firecrackerResetHandleVersion, OldID: request.Current.ID, OldVMDir: m.Handle.VMDir, OldPID: m.Process.PID, OldPIDStart: m.Process.StartTime, OldSocket: m.Handle.Socket, OldProcess: m.Process, NewID: "fc-r" + generation[:8], BaselinePath: request.Node.Image.ID, BaselineDigest: request.Baseline.Digest, NewIdentity: target}
	return substrate.ResetHandle{Provider: state, Request: request}, nil
}
func (s *Substrate) DestroyReset(ctx context.Context, handle substrate.ResetHandle) error {
	state, request, err := firecrackerResetState(handle)
	if err != nil {
		return err
	}
	if request.Current.ID != state.OldID {
		return fmt.Errorf("firecracker reset old identity conflict")
	}
	dir, err := s.manifestDirectory(request.Current)
	if err != nil {
		return err
	}
	if state.OldVMDir != dir {
		return fmt.Errorf("firecracker reset old directory identity conflict")
	}
	oldCtx, _, err := resetOperationContexts(ctx, state, request)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(state.OldVMDir); os.IsNotExist(err) {
		live, _, probeErr := probeProcess(state.OldProcess)
		if probeErr != nil {
			return probeErr
		}
		if live {
			return fmt.Errorf("firecracker reset old process still running after directory removal")
		}
		return nil
	}
	return s.DestroyNode(oldCtx, request.Current)
}
func (s *Substrate) ApplyReset(ctx context.Context, handle substrate.ResetHandle) (substrate.NodeHandle, error) {
	state, request, err := firecrackerResetState(handle)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	resolved, err := artifact.New().ResolveIdentity(artifact.IdentitySpec{Kind: substrate.ArtifactRootFS, Source: state.BaselinePath, ExpectedDigest: state.BaselineDigest, Architecture: request.Baseline.Architecture, GuestFamily: request.Baseline.GuestFamily})
	if err != nil || resolved.Identity.Digest != state.BaselineDigest {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker reset baseline changed: %w", err)
	}
	_, targetCtx, err := resetOperationContexts(ctx, state, request)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	ctx = targetCtx
	if state.NewHandle != nil {
		h := firecrackerNodeHandle(state.NewID, state.NewHandle)
		_, m, release, err := s.lockedManifest(ctx, h)
		if err != nil {
			return substrate.NodeHandle{}, err
		}
		defer release()
		if err := recoverLaunch(m); err != nil {
			return substrate.NodeHandle{}, err
		}
		if _, _, err := probeProcess(m.Process); err != nil {
			return substrate.NodeHandle{}, err
		}
		return manifestHandle(m), nil
	}
	// Creation may have committed its manifest before the caller received the
	// replacement handle. Discover that exact generation before creating again.
	var recovered substrate.NodeHandle
	if state.NewIdentity.TopologyUID != "" {
		recovered, err = s.DiscoverManagedNode(ctx, state.NewIdentity, request.Node.Name)
		if err != nil && !driver.IsCategory(err, driver.ErrorNotFound) {
			return substrate.NodeHandle{}, err
		}
	} else {
		candidate := substrate.NodeHandle{ID: state.NewID, Generation: state.NewIdentity.Generation}
		_, m, release, lookupErr := s.lockedManifest(ctx, candidate)
		if lookupErr == nil {
			recoveryErr := recoverLaunch(m)
			if recoveryErr == nil {
				_, _, recoveryErr = probeProcess(m.Process)
			}
			if recoveryErr == nil {
				recovered = manifestHandle(m)
			}
			release()
			if recoveryErr != nil {
				return substrate.NodeHandle{}, recoveryErr
			}
		} else if !errors.Is(lookupErr, errGenerationAbsent) {
			return substrate.NodeHandle{}, lookupErr
		}
	}
	if recovered.ID != "" {
		if recovered.ID != state.NewID {
			return substrate.NodeHandle{}, fmt.Errorf("firecracker reset replacement identity conflict")
		}
		state.NewHandle = recovered.Provider.(*HandleState)
		return recovered, nil
	}

	created, err := s.createNodeWithID(ctx, request.Node, state.NewID)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	hs, ok := created.Provider.(*HandleState)
	if !ok || hs == nil {
		return substrate.NodeHandle{}, fmt.Errorf("firecracker reset create returned invalid handle")
	}
	state.NewHandle = hs
	return created, nil
}

func (s *Substrate) ObserveReset(ctx context.Context, handle substrate.ResetHandle) (substrate.ResetObservation, error) {
	state, request, err := firecrackerResetState(handle)
	if err != nil {
		return substrate.ResetObservation{}, err
	}
	_, targetCtx, err := resetOperationContexts(ctx, state, request)
	if err != nil {
		return substrate.ResetObservation{}, err
	}
	ctx = targetCtx
	observation := substrate.ResetObservation{Phase: substrate.ResetPhaseApplying, OldExternalID: state.OldID, NewExternalID: state.NewID, BaselineDigest: state.BaselineDigest}
	if state.OldVMDir != "" {
		if _, err := os.Stat(state.OldVMDir); err == nil {
			observation.Residue = append(observation.Residue, state.OldVMDir)
		}
	}
	if state.OldPID > 0 && processMatches(state.OldPID, state.OldPIDStart) {
		observation.Residue = append(observation.Residue, fmt.Sprintf("pid:%d", state.OldPID))
	}
	if state.NewHandle == nil {
		observation.Reason = "replacement VM has not been prepared"
		return observation, nil
	}
	nodeObservation, err := s.ObserveNode(ctx, firecrackerNodeHandle(state.NewID, state.NewHandle))
	if err != nil {
		return observation, err
	}
	if !nodeObservation.Exists || !nodeObservation.Running || !nodeObservation.Healthy {
		observation.Reason = nodeObservation.Reason
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

// Runtime authorizes a reset using the replacement generation. Old resources
// retain their identity throughout recovery; never rewrite the caller's handle
// or loosen the ordinary manifest generation check to accommodate reset.
func validateResetTarget(target substrate.OperationIdentity, current *HandleState) error {
	if current == nil || current.Version != manifestVersion || current.OwnerUID == "" || current.ResourceUID == "" || current.Generation < 1 ||
		target.ActionID == "" || target.OwnerUID != current.OwnerUID || target.ResourceUID != current.ResourceUID || target.TopologyUID != current.TopologyUID ||
		target.Generation <= current.Generation || target.Generation-current.Generation != 1 {
		return fmt.Errorf("firecracker reset target identity must match current owner/resource and generation + 1")
	}
	return nil
}

func resetOperationContexts(ctx context.Context, state *resetHandleState, request substrate.ResetRequest) (context.Context, context.Context, error) {
	current, _ := request.Current.Provider.(*HandleState)
	if err := validateResetTarget(state.NewIdentity, current); err != nil {
		return nil, nil, err
	}
	if incoming, ok := substrate.OperationIdentityFromContext(ctx); ok && incoming != state.NewIdentity {
		return nil, nil, fmt.Errorf("firecracker reset operation identity differs from durable target identity")
	}
	old := state.NewIdentity
	old.Generation = current.Generation
	return substrate.WithOperationIdentity(ctx, old), substrate.WithOperationIdentity(ctx, state.NewIdentity), nil
}
func (s *Substrate) MarshalResetHandle(handle substrate.ResetHandle) (json.RawMessage, error) {
	state, ok := handle.Provider.(*resetHandleState)
	if !ok || state == nil {
		return nil, fmt.Errorf("firecracker reset handle has type %T", handle.Provider)
	}
	return json.Marshal(state)
}

func (s *Substrate) UnmarshalResetHandle(raw json.RawMessage) (substrate.ResetHandle, error) {
	var state resetHandleState
	if err := json.Unmarshal(raw, &state); err != nil {
		return substrate.ResetHandle{}, err
	}
	if state.Version != firecrackerResetHandleVersion {
		return substrate.ResetHandle{}, fmt.Errorf("firecracker reset handle version %d is unsupported", state.Version)
	}
	return substrate.ResetHandle{Provider: &state}, nil
}

func firecrackerResetState(handle substrate.ResetHandle) (*resetHandleState, substrate.ResetRequest, error) {
	state, ok := handle.Provider.(*resetHandleState)
	if !ok || state == nil || state.Version != firecrackerResetHandleVersion {
		return nil, substrate.ResetRequest{}, fmt.Errorf("invalid firecracker reset handle")
	}
	if handle.Request.Node.Name == "" {
		return nil, substrate.ResetRequest{}, fmt.Errorf("firecracker reset request was not injected by runtime")
	}
	return state, handle.Request, nil
}

func (s *Substrate) validateOwnedVMDir(vmID, vmDir string) error {
	if vmID == "" || vmDir == "" || filepath.Clean(vmDir) != filepath.Join(filepath.Clean(s.rootfsDir), vmID) {
		return fmt.Errorf("firecracker reset refuses unowned VM directory %q", vmDir)
	}
	return nil
}

func cloneHandleState(state *HandleState) *HandleState {
	if state == nil {
		return nil
	}
	cloned := *state
	return &cloned
}

func firecrackerNodeHandle(id string, state *HandleState) substrate.NodeHandle {
	conn := substrate.ConnInfo{}
	if state.VsockUDS != "" {
		conn = substrate.ConnInfo{Kind: substrate.ConnKindVsock, Endpoint: fmt.Sprintf("%s:%d", state.VsockUDS, state.VsockPort)}
	}
	return substrate.NodeHandle{ID: id, Generation: state.Generation, Provider: state, Conn: conn}
}

func (s *Substrate) restoreResetVMProcess(id string, state *HandleState) {
	h := firecrackerNodeHandle(id, state)
	vm, m, release, err := s.lockedManifest(context.Background(), h)
	if err != nil {
		return
	}
	defer release()
	if err := recoverLaunch(m); err != nil {
		return
	}
	live, _, err := probeProcess(m.Process)
	if err != nil {
		return
	}
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if vm.cmd != nil {
		return
	}
	vm.socket, vm.cfgPath, vm.rootfs = m.Handle.Socket, m.Handle.ConfigPath, filepath.Join(m.Handle.VMDir, "rootfs.ext4")
	vm.pid, vm.startTime, vm.netnsName, vm.started = m.Process.PID, m.Process.StartTime, m.Handle.NetnsName, live
}
