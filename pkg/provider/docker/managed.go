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
	if len(candidates) == 1 {
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
	if err := verifyLabels(inspected.Config.Labels, labels); err != nil {
		return substrate.NodeHandle{}, err
	}
	return managedHandle(inspected, id.Generation)
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
