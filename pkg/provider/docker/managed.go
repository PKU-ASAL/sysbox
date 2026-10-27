package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
)

func operationLabels(id substrate.OperationIdentity) map[string]string {
	return map[string]string{"sysbox.managed": "true", "sysbox.owner_uid": id.OwnerUID, "sysbox.resource_uid": id.ResourceUID, "sysbox.topology_uid": id.TopologyUID, "sysbox.generation": strconv.FormatInt(id.Generation, 10)}
}
func expectedLabels(ctx context.Context, labels map[string]string, creating bool) (map[string]string, error) {
	expected := make(map[string]string, len(labels)+6)
	for k, v := range labels {
		expected[k] = v
	}
	if id, ok := substrate.OperationIdentityFromContext(ctx); ok {
		if err := id.Validate(); err != nil {
			return nil, driver.Wrap(driver.ErrorInvalidState, "docker", "invalid operation identity", err)
		}
		for k, v := range operationLabels(id) {
			expected[k] = v
		}
		if creating {
			expected["sysbox.action_id"] = id.ActionID
		}
	}
	return expected, nil
}
func verifyLabels(actual, expected map[string]string) error {
	for key, value := range expected {
		if actual[key] != value {
			return driver.Wrap(driver.ErrorInvalidState, "docker", fmt.Sprintf("resource ownership label %s mismatch", key), nil)
		}
	}
	return nil
}

// DiscoverManagedNode queries all containers of one durable generation. A name
// collision and multiple matching resources are conflicts, never proof of absence.
func (s *Substrate) DiscoverManagedNode(ctx context.Context, id substrate.OperationIdentity, name string) (substrate.NodeHandle, error) {
	if err := id.Validate(); err != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "invalid discovery identity", err)
	}
	labels := operationLabels(id)
	query := filters.NewArgs()
	for k, v := range labels {
		query.Add("label", k+"="+v)
	}
	candidates, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: query})
	if err != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, "docker", "managed container query unavailable", err)
	}
	if len(candidates) > 1 {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "multiple containers match managed generation", nil)
	}
	target := name
	transferred, err := s.transferredContainerFor(id)
	if err != nil {
		return substrate.NodeHandle{}, err
	}
	if transferred != "" {
		target = transferred
	}
	if len(candidates) == 1 {
		if transferred != "" && transferred != candidates[0].ID {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "conflicting transferred generation", nil)
		}
		target = candidates[0].ID
	}
	inspected, err := s.cli.ContainerInspect(ctx, target)
	if err != nil {
		if len(candidates) == 0 && errdefs.IsNotFound(err) {
			return substrate.NodeHandle{}, driver.Wrap(driver.ErrorNotFound, "docker", "managed container absent", err)
		}
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, "docker", "managed container inspection unavailable", err)
	}
	if inspected.Config == nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "container lacks ownership metadata", nil)
	}
	actual, e := s.managedOwnershipLabels(substrate.WithOperationIdentity(ctx, id), inspected)
	if e != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "managed ownership evidence invalid", e)
	}
	if err := verifyLabels(actual, labels); err != nil {
		return substrate.NodeHandle{}, err
	}
	if transferred != "" {
		return s.adoptHandle(ctx, inspected, id), nil
	}
	return managedHandle(inspected, id.Generation)
}

// DeleteManagedExternal removes an orphan only after re-reading the complete
// ownership label set. It is the provider-level primitive used by fenced GC;
// callers must perform their durable inventory/epoch checks before invoking it.
func (s *Substrate) DeleteManagedExternal(ctx context.Context, externalID, ownerUID, resourceUID string, generation int64) error {
	if externalID == "" || ownerUID == "" || resourceUID == "" || generation < 1 {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "incomplete GC ownership identity", nil)
	}
	inspected, err := s.cli.ContainerInspect(ctx, externalID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return driver.Wrap(driver.ErrorUnavailable, "docker", "GC container inspection unavailable", err)
	}
	if inspected.ContainerJSONBase == nil || inspected.ID != externalID || inspected.Config == nil {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "GC container lacks ownership labels", nil)
	}
	expected := map[string]string{"sysbox.managed": "true", "sysbox.owner_uid": ownerUID, "sysbox.topology_uid": ownerUID, "sysbox.resource_uid": resourceUID, "sysbox.generation": strconv.FormatInt(generation, 10)}
	actual, err := s.managedOwnershipLabels(substrate.WithOperationIdentity(ctx, substrate.OperationIdentity{ActionID: "gc", OwnerUID: ownerUID, TopologyUID: ownerUID, ResourceUID: resourceUID, Generation: generation}), inspected)
	if err != nil {
		return err
	}
	if err := verifyLabels(actual, expected); err != nil {
		return err
	}
	if err := s.cli.ContainerRemove(ctx, externalID, container.RemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	_, err = s.cli.ContainerInspect(ctx, externalID)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return driver.Wrap(driver.ErrorUnavailable, "docker", "GC container absence unconfirmed", err)
	}
	return driver.Wrap(driver.ErrorInvalidState, "docker", "GC container remains after deletion", nil)
}

func managedHandle(inspected dockertypes.ContainerJSON, generation int64) (substrate.NodeHandle, error) {
	if inspected.ContainerJSONBase == nil || inspected.ID == "" || inspected.Config == nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorUnavailable, "docker", "incomplete managed container inspection", nil)
	}
	hs := &HandleState{}
	raw := inspected.Config.Labels["sysbox.node_state"]
	if raw == "" || json.Unmarshal([]byte(raw), hs) != nil {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "missing or corrupt managed container recovery state", nil)
	}
	if hs.ContainerName != strings.TrimPrefix(inspected.Name, "/") {
		return substrate.NodeHandle{}, driver.Wrap(driver.ErrorInvalidState, "docker", "managed container name changed", nil)
	}
	handle := substrate.NodeHandle{ID: inspected.ID, Generation: generation, Provider: hs, Conn: substrate.ConnInfo{Kind: substrate.ConnKindDocker, Endpoint: inspected.ID, User: "root"}}
	if inspected.NetworkSettings != nil {
		for _, endpoint := range inspected.NetworkSettings.Networks {
			if endpoint != nil && endpoint.IPAddress != "" {
				handle.Net.PrimaryIP = endpoint.IPAddress
				break
			}
		}
	}
	return handle, nil
}

var _ driver.ManagedDiscovery = (*Substrate)(nil)
