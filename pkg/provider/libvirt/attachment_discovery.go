package libvirt

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
	id, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || id.Validate() != nil {
		return nil, fmt.Errorf("owned generation required")
	}
	target, present, err := existingDomainUUID(ctx, h)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("domain missing")
	}
	hs := hsFrom(h)
	raw, err := virshQuery(ctx, "dumpxml", target)
	if err != nil {
		return nil, err
	}
	if len(hs.Bridges) != len(requests) {
		return nil, fmt.Errorf("incomplete bridge inventory")
	}
	if err = validateDomainInterfaces(raw, hs.Bridges); err != nil {
		return nil, err
	}
	result := map[string]driver.AttachmentResult{}
	for _, req := range requests {
		if req.Name == "" {
			return nil, fmt.Errorf("attachment name required")
		}
		if _, exists := result[req.Name]; exists {
			return nil, fmt.Errorf("duplicate attachment request")
		}
		var found *BridgeAttach
		for i := range hs.Bridges {
			b := &hs.Bridges[i]
			if b.Name == req.Name {
				if found != nil {
					return nil, fmt.Errorf("ambiguous bridge identity")
				}
				found = b
			}
		}
		if found == nil || found.MAC != req.MAC {
			return nil, fmt.Errorf("attachment identity conflict")
		}
		var netState networkState
		if json.Unmarshal(req.NetworkState, &netState) != nil {
			return nil, fmt.Errorf("invalid target network")
		}
		expected := network.BridgeTarget{Identity: req.NetworkIdentity, NetworkNamespace: netState.Netns, Bridge: netState.LibvirtBridge, Root: true}
		if netState.NAT || netState.DockerNetworkID != "" {
			if netState.ManagedBridge == nil || netState.ManagedBridge.NetworkID != netState.DockerNetworkID {
				return nil, fmt.Errorf("managed bridge missing")
			}
			expected = network.BridgeTarget{Identity: req.NetworkIdentity, Bridge: netState.ManagedBridge.Bridge, Root: true, Managed: netState.ManagedBridge}
		}
		actual := found.Target
		actual.Identity.ActionID = expected.Identity.ActionID
		if !reflect.DeepEqual(actual, expected) {
			return nil, fmt.Errorf("attachment target conflicts with admitted network")
		}
		stateRaw, _ := json.Marshal(attachmentState{Bridge: found.Bridge, MAC: found.MAC})
		observed, err := s.Observe(ctx, h, req, stateRaw)
		if err != nil {
			return nil, err
		}
		result[req.Name] = observed
	}
	after, err := virshQuery(ctx, "dumpxml", target)
	if err != nil {
		return nil, err
	}
	if string(raw) != string(after) {
		return nil, fmt.Errorf("domain inventory changed during observation")
	}
	return result, nil
}
