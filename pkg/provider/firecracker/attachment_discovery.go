package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

func (s *Substrate) DiscoverManagedAttachments(ctx context.Context, h substrate.NodeHandle, requests []driver.AttachmentRequest) (map[string]driver.AttachmentResult, error) {
	_, m, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		return nil, err
	}
	defer release()
	if m.Phase == "attaching" || m.Phase == "starting" {
		return nil, fmt.Errorf("incomplete attachment transition")
	}
	raw, err := readRegularFile(m.Handle.ConfigPath)
	if err != nil {
		return nil, err
	}
	var cfg fcConfig
	if err = json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if len(m.Handle.Attachments) != len(requests) || len(cfg.NetworkInterfaces) != len(requests) {
		return nil, fmt.Errorf("attachment inventory mismatch")
	}
	result := map[string]driver.AttachmentResult{}
	for _, req := range requests {
		if req.Name == "" {
			return nil, fmt.Errorf("attachment name required")
		}
		if _, exists := result[req.Name]; exists {
			return nil, fmt.Errorf("duplicate attachment request")
		}
		var found *managedAttachment
		for i := range m.Handle.Attachments {
			a := &m.Handle.Attachments[i]
			if a.Name == req.Name {
				if found != nil {
					return nil, fmt.Errorf("ambiguous attachment")
				}
				found = a
			}
		}
		if found == nil {
			return nil, fmt.Errorf("missing attachment")
		}
		var target networkState
		if err = json.Unmarshal(req.NetworkState, &target); err != nil {
			return nil, err
		}
		expected, err := attachmentBridgeTarget(req.NetworkIdentity, target)
		if err != nil {
			return nil, err
		}
		actual := found.Target
		actual.Identity.ActionID = expected.Identity.ActionID
		if !reflect.DeepEqual(actual, expected) {
			return nil, fmt.Errorf("attachment target generation mismatch")
		}
		matches := 0
		for _, nic := range cfg.NetworkInterfaces {
			if nic.IfaceID == found.GuestDevice && nic.HostDevName == found.Link.Name && nic.GuestMAC == req.MAC {
				matches++
			}
		}
		if matches != 1 {
			return nil, fmt.Errorf("attachment config identity mismatch")
		}
		bridge, err := network.OpenOwnedBridge(ctx, expected)
		if err != nil {
			return nil, err
		}
		alias, err := network.AttachmentAlias(substrate.OperationIdentity{ActionID: "discover", OwnerUID: m.OwnerUID, TopologyUID: m.TopologyUID, ResourceUID: m.ResourceUID, Generation: m.Generation}, req.Name)
		if err == nil {
			err = bridge.ObserveAttachment(ctx, found.Link, alias)
		}
		bridge.Close()
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(attachmentState{Tap: found.Link.Name, NetNS: found.Link.Namespace, GuestDevice: found.GuestDevice, Link: &found.Link})
		if err != nil {
			return nil, err
		}
		result[req.Name] = driver.AttachmentResult{Driver: "firecracker", GuestDevice: found.GuestDevice, State: raw}
	}
	return result, nil
}
