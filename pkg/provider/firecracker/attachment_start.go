package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// Validate the complete cold-plug configuration before launching any process.
func lockStartAttachments(ctx context.Context, m *vmManifest) (func(), error) {
	raw, err := readRegularFile(m.Handle.ConfigPath)
	if err != nil {
		return nil, err
	}
	var cfg fcConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
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
	for i, a := range attachments {
		alias, err := network.AttachmentAlias(id, a.Name)
		if err == nil {
			err = bridges[i].ObserveAttachment(ctx, a.Link, alias)
		}
		if err != nil {
			unlock()
			return nil, err
		}
	}
	return unlock, nil
}
