package docker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"

	"github.com/oslab/sysbox/pkg/substrate"
)

const dockerResetHandleVersion = 1

type resetHandleState struct {
	Version             int               `json:"version"`
	OldContainerID      string            `json:"old_container_id"`
	ContainerName       string            `json:"container_name"`
	BaselineDigest      string            `json:"baseline_digest"`
	Ownership           map[string]string `json:"ownership"`
	ImageCmd            []string          `json:"image_cmd,omitempty"`
	ImageEntrypoint     []string          `json:"image_entrypoint,omitempty"`
	RemoveDefaultBridge bool              `json:"remove_default_bridge,omitempty"`
}

func (s *Substrate) PrepareReset(ctx context.Context, request substrate.ResetRequest) (substrate.ResetHandle, error) {
	if err := validateDockerResetRequest(request); err != nil {
		return substrate.ResetHandle{}, err
	}
	state := &resetHandleState{
		Version: dockerResetHandleVersion, OldContainerID: request.Current.ID,
		ContainerName: request.Node.Name, BaselineDigest: request.Baseline.Digest,
		Ownership: copyResetLabels(request.Node.Labels),
	}
	_, portBindings, err := dockerPortConfig(request.Node.Ports)
	if err != nil {
		return substrate.ResetHandle{}, err
	}
	state.RemoveDefaultBridge = len(portBindings) > 0 || request.Node.ManagedNetwork
	if imageInfo, _, inspectErr := s.cli.ImageInspectWithRaw(ctx, request.Node.Image.ID); inspectErr != nil {
		return substrate.ResetHandle{}, fmt.Errorf("docker reset inspect baseline image: %w", inspectErr)
	} else if imageInfo.Config != nil {
		cfg, _ := request.Node.ProviderConfig.(*Config)
		state.ImageEntrypoint, state.ImageCmd = effectiveLaunch(imageInfo.Config.Entrypoint, imageInfo.Config.Cmd, cfg)
	}
	return substrate.ResetHandle{Provider: state, Request: request}, nil
}

func (s *Substrate) DestroyReset(ctx context.Context, handle substrate.ResetHandle) error {
	state, _, err := dockerResetState(handle)
	if err != nil {
		return err
	}
	if state.OldContainerID != "" {
		inspected, err := s.cli.ContainerInspect(ctx, state.OldContainerID)
		if err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("docker reset inspect old container: %w", err)
		}
		if err == nil {
			if err := requireResetOwnership(inspected.Config.Labels, state.Ownership); err != nil {
				return err
			}
			if err := s.cli.ContainerRemove(ctx, state.OldContainerID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("docker reset remove old container: %w", err)
			}
		}
	}
	return nil
}

func (s *Substrate) ApplyReset(ctx context.Context, handle substrate.ResetHandle) (substrate.NodeHandle, error) {
	state, request, err := dockerResetState(handle)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if inspected, inspectErr := s.cli.ContainerInspect(ctx, state.ContainerName); inspectErr == nil {
		if err := requireResetOwnership(inspected.Config.Labels, state.Ownership); err != nil {
			return substrate.NodeHandle{}, err
		}
		if inspected.ID == state.OldContainerID {
			return substrate.NodeHandle{}, fmt.Errorf("docker reset old container %s still occupies %q", state.OldContainerID, state.ContainerName)
		}
		if normalizeDigest(inspected.Image) != normalizeDigest(state.BaselineDigest) {
			return substrate.NodeHandle{}, fmt.Errorf("docker reset container %s uses image %s, want %s", inspected.ID, inspected.Image, state.BaselineDigest)
		}
		return dockerHandleFromResetState(inspected.ID, state), nil
	} else if !errdefs.IsNotFound(inspectErr) {
		return substrate.NodeHandle{}, fmt.Errorf("docker reset inspect replacement: %w", inspectErr)
	}
	return s.createNode(ctx, request.Node, true)
}

func (s *Substrate) ObserveReset(ctx context.Context, handle substrate.ResetHandle) (substrate.ResetObservation, error) {
	state, _, err := dockerResetState(handle)
	if err != nil {
		return substrate.ResetObservation{}, err
	}
	observation := substrate.ResetObservation{Phase: substrate.ResetPhaseApplying, OldExternalID: state.OldContainerID, BaselineDigest: state.BaselineDigest}
	if state.OldContainerID != "" {
		if old, inspectErr := s.cli.ContainerInspect(ctx, state.OldContainerID); inspectErr == nil {
			observation.Residue = append(observation.Residue, old.ID)
		} else if !errdefs.IsNotFound(inspectErr) {
			return observation, inspectErr
		}
	}
	replacement, err := s.cli.ContainerInspect(ctx, state.ContainerName)
	if err != nil {
		if errdefs.IsNotFound(err) {
			observation.Reason = "replacement container not created"
			return observation, nil
		}
		return observation, err
	}
	if err := requireResetOwnership(replacement.Config.Labels, state.Ownership); err != nil {
		return observation, err
	}
	observation.NewExternalID = replacement.ID
	if normalizeDigest(replacement.Image) != normalizeDigest(state.BaselineDigest) {
		observation.Reason = fmt.Sprintf("replacement image %s does not match baseline %s", replacement.Image, state.BaselineDigest)
		return observation, nil
	}
	if len(observation.Residue) == 0 {
		observation.Phase = substrate.ResetPhaseComplete
		observation.Converged = true
	}
	return observation, nil
}

func (s *Substrate) CleanupReset(ctx context.Context, handle substrate.ResetHandle) error {
	state, _, err := dockerResetState(handle)
	if err != nil {
		return err
	}
	if state.OldContainerID == "" {
		return nil
	}
	old, err := s.cli.ContainerInspect(ctx, state.OldContainerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := requireResetOwnership(old.Config.Labels, state.Ownership); err != nil {
		return err
	}
	return s.cli.ContainerRemove(ctx, state.OldContainerID, container.RemoveOptions{Force: true})
}

func (s *Substrate) MarshalResetHandle(handle substrate.ResetHandle) (json.RawMessage, error) {
	state, ok := handle.Provider.(*resetHandleState)
	if !ok || state == nil {
		return nil, fmt.Errorf("docker reset handle has type %T", handle.Provider)
	}
	return json.Marshal(state)
}

func (s *Substrate) UnmarshalResetHandle(raw json.RawMessage) (substrate.ResetHandle, error) {
	var state resetHandleState
	if err := json.Unmarshal(raw, &state); err != nil {
		return substrate.ResetHandle{}, fmt.Errorf("docker decode reset handle: %w", err)
	}
	if state.Version != dockerResetHandleVersion {
		return substrate.ResetHandle{}, fmt.Errorf("docker reset handle version %d is unsupported", state.Version)
	}
	return substrate.ResetHandle{Provider: &state}, nil
}

func dockerResetState(handle substrate.ResetHandle) (*resetHandleState, substrate.ResetRequest, error) {
	state, ok := handle.Provider.(*resetHandleState)
	if !ok || state == nil {
		return nil, substrate.ResetRequest{}, fmt.Errorf("docker reset handle has type %T", handle.Provider)
	}
	if state.Version != dockerResetHandleVersion {
		return nil, substrate.ResetRequest{}, fmt.Errorf("docker reset handle version %d is unsupported", state.Version)
	}
	if handle.Request.Node.Name == "" {
		return nil, substrate.ResetRequest{}, fmt.Errorf("docker reset request was not injected by runtime")
	}
	return state, handle.Request, nil
}

func validateDockerResetRequest(request substrate.ResetRequest) error {
	if request.Baseline.Kind != substrate.ArtifactOCI {
		return fmt.Errorf("docker reset requires OCI baseline")
	}
	if request.Node.Name == "" || request.Node.Image.ID == "" {
		return fmt.Errorf("docker reset requires node name and immutable image ID")
	}
	if normalizeDigest(request.Node.Image.Identity.Digest) != normalizeDigest(request.Baseline.Digest) || normalizeDigest(request.Node.Image.ID) != normalizeDigest(request.Baseline.Digest) {
		return fmt.Errorf("docker reset node image does not match pinned baseline %s", request.Baseline.Digest)
	}
	return requireResetOwnership(request.Node.Labels, request.Node.Labels)
}

func requireResetOwnership(actual, expected map[string]string) error {
	if actual["sysbox.managed"] != "true" || expected["sysbox.managed"] != "true" {
		return fmt.Errorf("docker reset refuses container without sysbox.managed=true ownership")
	}
	for key, value := range expected {
		if actual[key] != value {
			return fmt.Errorf("docker reset ownership label %s mismatch", key)
		}
	}
	return nil
}

func copyResetLabels(labels map[string]string) map[string]string {
	copy := make(map[string]string, 5)
	for _, key := range []string{"sysbox.managed", "sysbox.topology", "sysbox.resource", "sysbox.resource_type", "sysbox.resource_name"} {
		if value := labels[key]; value != "" {
			copy[key] = value
		}
	}
	return copy
}

func dockerHandleFromResetState(id string, state *resetHandleState) substrate.NodeHandle {
	return substrate.NodeHandle{ID: id, Provider: &HandleState{
		ContainerName: state.ContainerName, ImageCmd: append([]string(nil), state.ImageCmd...),
		ImageEntrypoint: append([]string(nil), state.ImageEntrypoint...), RemoveDefaultBridge: state.RemoveDefaultBridge,
	}, Conn: substrate.ConnInfo{Kind: substrate.ConnKindDocker, Endpoint: id}}
}
