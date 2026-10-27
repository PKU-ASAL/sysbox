package docker

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/oslab/sysbox/pkg/driver"
	ownednetwork "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
	"reflect"
)

const managedBridgeLabel = "sysbox.managed_bridge"

func (*Substrate) PreflightManagedBridge(ctx context.Context) error {
	return ownednetwork.PreflightManagedBridge(ctx)
}

func (s *Substrate) createExposedManagedNetwork(ctx context.Context, spec substrate.ManagedNetworkSpec) (substrate.ManagedNetworkInfo, error) {
	if err := s.PreflightManagedBridge(ctx); err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok {
		return substrate.ManagedNetworkInfo{}, fmt.Errorf("managed bridge requires operation identity")
	}
	if err := identity.Validate(); err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	unlock, err := ownednetwork.LockManagedNetworks(ctx)
	if err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	defer unlock()
	name := "sysbox-nat-" + spec.Name
	existing, err := s.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err == nil {
		spec.Name = existing.ID
		return s.ReadManagedNetwork(ctx, spec)
	}
	if !errdefs.IsNotFound(err) {
		return substrate.ManagedNetworkInfo{}, err
	}
	labels := make(map[string]string, len(spec.Labels)+1)
	for key, value := range spec.Labels {
		labels[key] = value
	}
	labels[managedBridgeLabel] = "v1"
	bridge := ownednetwork.ManagedBridgeName(identity)
	id, err := s.createBridgeNetwork(ctx, name, spec.CIDR, labels, bridge)
	if err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	proof, err := ownednetwork.RecordManagedBridge(ctx, "docker", id, bridge, identity)
	if err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	if err := s.VerifyManagedBridge(ctx, *proof); err != nil {
		return substrate.ManagedNetworkInfo{}, err
	}
	return substrate.ManagedNetworkInfo{ID: id, Name: name, BridgeTarget: proof}, nil
}

func (s *Substrate) VerifyManagedBridge(ctx context.Context, proof substrate.ManagedBridgeTarget) error {
	if proof.Provider != "docker" || proof.NetworkID == "" || proof.Version != 1 {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "invalid managed bridge target", nil)
	}
	anchor, err := ownednetwork.LoadManagedBridge("docker", proof.NetworkID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*anchor, proof) {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "managed bridge anchor conflict", nil)
	}
	existing, err := s.cli.NetworkInspect(ctx, proof.NetworkID, network.InspectOptions{})
	if err != nil {
		return err
	}
	expected, err := expectedLabels(substrate.WithOperationIdentity(ctx, proof.Identity), nil, false)
	if err != nil {
		return err
	}
	if err := verifyLabels(existing.Labels, expected); err != nil {
		return err
	}
	if existing.ID != proof.NetworkID || existing.Driver != "bridge" || existing.Labels[managedBridgeLabel] != "v1" || existing.Options["com.docker.network.bridge.name"] != proof.Bridge {
		return driver.Wrap(driver.ErrorInvalidState, "docker", "managed bridge provider identity conflict", nil)
	}
	return nil
}

func (s *Substrate) removeManagedNetwork(ctx context.Context, id string) error {
	existing, err := s.cli.NetworkInspect(ctx, id, network.InspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	expected, err := expectedLabels(ctx, nil, false)
	if err != nil {
		return err
	}
	if err := verifyLabels(existing.Labels, expected); err != nil {
		return err
	}
	if existing.Labels[managedBridgeLabel] != "v1" {
		return s.RemoveBridgeNetwork(ctx, id)
	}
	unlock, err := ownednetwork.LockManagedNetworks(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	proof, err := ownednetwork.LoadManagedBridge("docker", id)
	if err != nil {
		return err
	}
	if err := s.VerifyManagedBridge(ctx, *proof); err != nil {
		return err
	}
	if err := ownednetwork.InspectManagedBridge(ctx, *proof); err != nil {
		return err
	}
	if err := s.RemoveBridgeNetwork(ctx, id); err != nil {
		return err
	}
	return ownednetwork.RemoveManagedBridge("docker", id)
}
