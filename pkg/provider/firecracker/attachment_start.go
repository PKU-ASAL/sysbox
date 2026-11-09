package firecracker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Validate the complete cold-plug configuration before launching any process.
func lockStartAttachments(ctx context.Context, m *vmManifest) (func(), error) {
	return lockStartAttachmentsForUID(ctx, m, 0)
}

// A reserved UID cannot be recycled while it still owns an earlier TAP.
func revokeJailTapAccess(m *vmManifest) error {
	ctx := context.Background()
	id := substrate.OperationIdentity{ActionID: "release-jail", OwnerUID: m.OwnerUID, TopologyUID: m.TopologyUID, ResourceUID: m.ResourceUID, Generation: m.Generation}
	for _, a := range m.Handle.Attachments {
		if m.Handle.Fabric != nil {
			if a.Circuit == nil {
				return fmt.Errorf("missing fabric proof")
			}
			if err := network.VMCircuitAbsent(ctx, *m.Handle.Fabric, *a.Circuit); err == nil {
				continue
			}
			bridge, err := network.OpenOwnedBridge(ctx, a.Target)
			if err != nil {
				return err
			}
			err = bridge.SetVMCircuitTapOwner(ctx, *m.Handle.Fabric, *a.Circuit, 0)
			bridge.Close()
			if err != nil {
				return err
			}
			continue
		}
		alias, err := network.AttachmentAlias(id, a.Name)
		if err != nil {
			return err
		}
		if err = network.ObserveOwnedAttachment(ctx, a.Link, alias); driver.IsCategory(err, driver.ErrorNotFound) {
			continue
		} else if err != nil {
			return err
		}
		bridge, err := network.OpenOwnedBridge(ctx, a.Target)
		if err != nil {
			return err
		}
		err = bridge.SetTapOwner(ctx, a.Link, alias, 0)
		bridge.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func lockStartAttachmentsForUID(ctx context.Context, m *vmManifest, uid uint32) (func(), error) {
	raw, err := readRegularFile(m.Handle.ConfigPath)
	if err != nil {
		return nil, err
	}
	var cfg fcConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if err := validateFabricHandle(m.Handle, manifestNetworkIdentity(m)); err != nil {
		return nil, err
	}
	attachments := m.Handle.Attachments
	if len(cfg.NetworkInterfaces) != len(attachments) || m.Handle.NICCount != len(attachments) {
		return nil, fmt.Errorf("network attachment proofs missing; rebuild VM")
	}
	if len(attachments) == 0 {
		return func() {}, nil
	}
	targets := make([]network.BridgeTarget, 0, len(attachments))
	for i, a := range attachments {
		if a.Name == "" || a.GuestDevice != cfg.NetworkInterfaces[i].IfaceID || a.Link.Name != cfg.NetworkInterfaces[i].HostDevName || a.Link.Namespace != m.Handle.NetnsName {
			return nil, fmt.Errorf("VM network config conflicts with attachment proofs")
		}
		targets = append(targets, a.Target)
	}
	bridges, unlock, err := network.LockBridgeTargets(ctx, targets)
	if err != nil {
		return nil, err
	}
	id := substrate.OperationIdentity{ActionID: "start", OwnerUID: m.OwnerUID, TopologyUID: m.TopologyUID, ResourceUID: m.ResourceUID, Generation: m.Generation}
	if m.Handle.Fabric != nil {
		circuits := make([]network.VMCircuit, 0, len(attachments))
		for _, a := range attachments {
			circuits = append(circuits, *a.Circuit)
		}
		if err := network.VerifyVMCircuitInventory(ctx, *m.Handle.Fabric, circuits); err != nil {
			unlock()
			return nil, err
		}
	}
	for i, a := range attachments {
		if m.Handle.Fabric != nil {
			err := bridges[i].ObserveVMCircuit(ctx, *m.Handle.Fabric, *a.Circuit)
			if err == nil && uid != 0 {
				err = bridges[i].SetVMCircuitTapOwner(ctx, *m.Handle.Fabric, *a.Circuit, uid)
			}
			if err != nil {
				unlock()
				return nil, err
			}
			continue
		}
		alias, err := network.AttachmentAlias(id, a.Name)
		if err == nil {
			err = bridges[i].ObserveAttachment(ctx, a.Link, alias)
		}
		if err == nil && uid != 0 {
			err = bridges[i].SetTapOwner(ctx, a.Link, alias, uid)
		}
		if err != nil {
			unlock()
			return nil, err
		}
	}
	return unlock, nil
}
