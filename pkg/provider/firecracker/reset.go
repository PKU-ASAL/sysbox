package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/substrate"
)

const firecrackerResetHandleVersion = 1

type resetHandleState struct {
	Version        int          `json:"version"`
	OldID          string       `json:"old_id"`
	OldVMDir       string       `json:"old_vm_dir"`
	OldPID         int          `json:"old_pid,omitempty"`
	OldPIDStart    string       `json:"old_pid_start,omitempty"`
	OldSocket      string       `json:"old_socket,omitempty"`
	NewID          string       `json:"new_id"`
	BaselinePath   string       `json:"baseline_path"`
	BaselineDigest string       `json:"baseline_digest"`
	NewHandle      *HandleState `json:"new_handle,omitempty"`
}

func (s *Substrate) PrepareReset(_ context.Context, request substrate.ResetRequest) (substrate.ResetHandle, error) {
	if request.Baseline.Kind != substrate.ArtifactRootFS || request.Node.Image.ID == "" || request.Node.Image.Identity.Digest != request.Baseline.Digest {
		return substrate.ResetHandle{}, fmt.Errorf("firecracker reset requires a pinned rootfs baseline")
	}
	current, ok := request.Current.Provider.(*HandleState)
	if !ok || current == nil {
		return substrate.ResetHandle{}, fmt.Errorf("firecracker reset requires typed current handle state")
	}
	if err := s.validateOwnedVMDir(request.Current.ID, current.VMDir); err != nil {
		return substrate.ResetHandle{}, err
	}
	if cfg, ok := request.Node.ProviderConfig.(*Config); ok && cfg != nil && cfg.Rootfs != "" && filepath.Clean(cfg.Rootfs) != filepath.Clean(request.Node.Image.ID) {
		return substrate.ResetHandle{}, fmt.Errorf("firecracker reset rootfs override does not match pinned baseline")
	}
	anchor := readProcessAnchor(current.PIDFile)
	oldPID, oldPIDStart, oldSocket := current.PID, current.PIDStart, current.Socket
	if anchor.PID > 0 {
		if anchor.VMID == "" || anchor.StartTime == "" || anchor.Socket == "" || anchor.VMID != request.Current.ID {
			return substrate.ResetHandle{}, fmt.Errorf("firecracker reset refuses incomplete or mismatched old process anchor")
		}
		oldPID, oldPIDStart, oldSocket = anchor.PID, anchor.StartTime, anchor.Socket
	}
	generation := uuid.NewString()
	state := &resetHandleState{
		Version: firecrackerResetHandleVersion, OldID: request.Current.ID, OldVMDir: current.VMDir,
		OldPID: oldPID, OldPIDStart: oldPIDStart, OldSocket: oldSocket,
		NewID: "fc-r" + generation[:8], BaselinePath: request.Node.Image.ID,
		BaselineDigest: request.Baseline.Digest,
	}
	return substrate.ResetHandle{Provider: state, Request: request}, nil
}

func (s *Substrate) DestroyReset(ctx context.Context, handle substrate.ResetHandle) error {
	state, request, err := firecrackerResetState(handle)
	if err != nil {
		return err
	}
	if err := s.validateOwnedVMDir(state.OldID, state.OldVMDir); err != nil {
		return err
	}
	if _, statErr := os.Stat(state.OldVMDir); os.IsNotExist(statErr) {
		if state.OldPID > 0 && processMatches(state.OldPID, state.OldPIDStart) {
			return fmt.Errorf("firecracker reset old process is still running after VM directory removal")
		}
		return nil
	} else if statErr != nil {
		return statErr
	}
	anchor := readProcessAnchor(filepath.Join(state.OldVMDir, "firecracker.pid"))
	if state.OldPID > 0 {
		if state.OldPIDStart == "" || state.OldSocket == "" || anchor.PID != state.OldPID || anchor.StartTime != state.OldPIDStart || anchor.VMID != state.OldID || anchor.Socket != state.OldSocket {
			return fmt.Errorf("firecracker reset old process ownership anchor mismatch")
		}
	} else if anchor.PID > 0 {
		return fmt.Errorf("firecracker reset found unexpected old process anchor")
	}
	if err := s.DestroyNode(ctx, request.Current); err != nil {
		return err
	}
	if (state.OldPID > 0 && processMatches(state.OldPID, state.OldPIDStart)) || (anchor.PID > 0 && processMatches(anchor.PID, anchor.StartTime)) {
		return fmt.Errorf("firecracker reset old process is still running")
	}
	if _, err := os.Stat(state.OldVMDir); err == nil {
		return fmt.Errorf("firecracker reset old VM directory still exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
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
	if state.NewHandle != nil {
		anchor := readProcessAnchor(state.NewHandle.PIDFile)
		if anchor.PID > 0 && anchor.VMID == state.NewID && processMatches(anchor.PID, anchor.StartTime) {
			state.NewHandle.PID = anchor.PID
			state.NewHandle.PIDStart = anchor.StartTime
			state.NewHandle.Ready = true
		}
		state.NewHandle.NICCount = 0
		handle := firecrackerNodeHandle(state.NewID, state.NewHandle)
		s.restoreResetVMProcess(state.NewID, state.NewHandle)
		return handle, nil
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
	state, _, err := firecrackerResetState(handle)
	if err != nil {
		return substrate.ResetObservation{}, err
	}
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

func (s *Substrate) CleanupReset(_ context.Context, handle substrate.ResetHandle) error {
	state, _, err := firecrackerResetState(handle)
	if err != nil {
		return err
	}
	if state.OldVMDir == "" {
		return nil
	}
	if err := s.validateOwnedVMDir(state.OldID, state.OldVMDir); err != nil {
		return err
	}
	return os.RemoveAll(state.OldVMDir)
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
	return substrate.NodeHandle{ID: id, Provider: state, Conn: conn}
}

func (s *Substrate) restoreResetVMProcess(id string, state *HandleState) {
	vmMu.Lock()
	defer vmMu.Unlock()
	if _, exists := vmStore[id]; exists {
		return
	}
	started := state.PID > 0 && processMatches(state.PID, state.PIDStart)
	vmStore[id] = &vmProcess{vmID: id, socket: state.Socket, rootfs: filepath.Join(state.VMDir, "rootfs.ext4"), cfgPath: state.ConfigPath, pid: state.PID, startTime: state.PIDStart, netnsName: state.NetnsName, started: started}
}
