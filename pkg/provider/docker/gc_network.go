package docker

import (
	"context"
	"strconv"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/driver"
)

// DeleteManagedNetworkExternal removes an orphaned Docker network only after
// re-reading its complete ownership labels. It is the network counterpart of
// DeleteManagedExternal and is intended for fenced host-supervisor GC.
func (s *Substrate) DeleteManagedNetworkExternal(ctx context.Context, externalID, ownerUID, resourceUID string, generation int64) error {
	if externalID == "" || ownerUID == "" || resourceUID == "" || generation < 1 {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "incomplete GC network ownership identity", nil)
	}
	inspected, err := s.cli.NetworkInspect(ctx, externalID, network.InspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return driver.Wrap(driver.ErrorUnavailable, "docker", "GC network inspection unavailable", err)
	}
	expected := map[string]string{
		"sysbox.managed": "true", "sysbox.owner_uid": ownerUID,
		"sysbox.topology_uid": ownerUID, "sysbox.resource_uid": resourceUID,
		"sysbox.generation": strconv.FormatInt(generation, 10),
	}
	if inspected.ID != externalID {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "GC network exact identity mismatch", nil)
	}
	if err := verifyLabels(inspected.Labels, expected); err != nil {
		return err
	}
	if err := s.cli.NetworkRemove(ctx, externalID); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	_, err = s.cli.NetworkInspect(ctx, externalID, network.InspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return driver.Wrap(driver.ErrorUnavailable, "docker", "GC network absence unconfirmed", err)
	}
	return driver.Wrap(driver.ErrorInvalidState, "docker", "GC network remains after deletion", nil)
}
