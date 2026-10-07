package libvirt

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os/exec"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// AttachNIC records the bridge in HandleState.Bridges so that StartNode
// can include the interface in the domain XML. Libvirt creates the TAP
// device and attaches it to the bridge when the domain starts.
type attachmentState struct {
	Bridge string `json:"bridge"`
	MAC    string `json:"mac"`
}
type networkState struct {
	NAT             bool   `json:"nat"`
	DockerNetworkID string `json:"docker_network_id"`
	Netns           string `json:"netns"`
	LibvirtBridge   string `json:"libvirt_bridge"`
}

func (s *Substrate) Attach(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest) (driver.AttachmentResult, error) {
	if err := validateHandleIdentity(ctx, h); err != nil {
		return driver.AttachmentResult{}, err
	}
	if err := req.NetworkIdentity.Validate(); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "libvirt", "target network identity required; rebuild old state", err)
	}
	var target networkState
	if err := json.Unmarshal(req.NetworkState, &target); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "libvirt", "decode network state", err)
	}
	hs := hsFrom(h)
	bridge := target.LibvirtBridge
	if target.NAT || target.DockerNetworkID != "" {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorUnsupported, "libvirt", "Docker NAT bridge attachment requires a verifiable bridge contract; use an isolated network", nil)
	}
	if bridge == "" || target.Netns == "" {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "libvirt", "owned root bridge target required; rebuild network", nil)
	}
	hs.Bridges = append(hs.Bridges, BridgeAttach{Target: network.BridgeTarget{Identity: req.NetworkIdentity, NetworkNamespace: target.Netns, Bridge: bridge, Root: true}, Name: req.Name, Netns: target.Netns, Bridge: bridge, MAC: req.MAC, IPPrefixes: append([]string(nil), req.IPPrefixes...), Gateway: req.Gateway})
	raw, _ := json.Marshal(attachmentState{Bridge: bridge, MAC: req.MAC})
	return driver.AttachmentResult{Driver: "libvirt", State: raw}, nil
}
func (s *Substrate) Observe(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest, raw json.RawMessage) (driver.AttachmentResult, error) {
	target, exists, err := existingDomainUUID(ctx, h)
	if err != nil {
		return driver.AttachmentResult{}, err
	}
	if !exists {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorNotFound, subName, "domain absent", nil)
	}
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "libvirt", "decode attachment state", err)
	}
	found := false
	for _, bridge := range hsFrom(h).Bridges {
		if bridge.Name == req.Name && bridge.Bridge == st.Bridge && bridge.MAC == st.MAC {
			if !sameGeneration(bridge.Target.Identity, req.NetworkIdentity) {
				return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, subName, "attachment target generation conflict", nil)
			}
			target, err := network.OpenOwnedBridge(ctx, bridge.Target)
			if err != nil {
				return driver.AttachmentResult{}, err
			}
			defer target.Close()
			found = true
			break
		}
	}
	if !found {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorNotFound, "libvirt", "attachment bridge not found", nil)
	}
	// Persisted bridge/MAC state is only an intent. When a domain identity is
	// available, verify the actual libvirt XML so external NIC changes become
	// observable drift instead of false Present results.
	if hs := hsFrom(h); hs.DomainName != "" {
		out, err := exec.CommandContext(ctx, "virsh", "dumpxml", target).CombinedOutput()
		if err != nil {
			return driver.AttachmentResult{}, driver.Wrap(driver.ErrorUnavailable, "libvirt", "observe domain interfaces", fmt.Errorf("%w: %s", err, out))
		}
		if !domainXMLHasInterface(out, st.Bridge, st.MAC) {
			return driver.AttachmentResult{}, driver.Wrap(driver.ErrorNotFound, "libvirt", "attachment interface not found", nil)
		}
	}
	return driver.AttachmentResult{Driver: "libvirt", State: raw}, nil
}

func domainXMLHasInterface(raw []byte, bridge, mac string) bool {
	var domain domainXML
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return false
	}
	for _, iface := range domain.Devices.Interfaces {
		if iface.Source.Bridge == bridge && (mac == "" || (iface.MAC != nil && iface.MAC.Address == mac)) {
			return true
		}
	}
	return false
}

func validateDomainInterfaces(raw []byte, bridges []BridgeAttach) error {
	var domain domainXML
	if err := xml.Unmarshal(raw, &domain); err != nil {
		return err
	}
	if len(domain.Devices.Interfaces) != len(bridges) {
		return driver.Wrap(driver.ErrorInvalidState, subName, "domain interface count conflicts with proven targets", nil)
	}
	matched := make([]bool, len(bridges))
	for _, iface := range domain.Devices.Interfaces {
		found := false
		for i, b := range bridges {
			if !matched[i] && iface.Type == "bridge" && iface.Source.Bridge == b.Bridge && (b.MAC == "" || (iface.MAC != nil && iface.MAC.Address == b.MAC)) {
				matched[i] = true
				found = true
				break
			}
		}
		if !found {
			return driver.Wrap(driver.ErrorInvalidState, subName, "domain interface conflicts with proven bridge target", nil)
		}
	}
	return nil
}
func (s *Substrate) Delete(ctx context.Context, h substrate.NodeHandle, _ driver.AttachmentRequest, raw json.RawMessage) error {
	if err := validateHandleIdentity(ctx, h); err != nil {
		return err
	}
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.Wrap(driver.ErrorInvalidState, "libvirt", "decode attachment state", err)
	}
	hs := hsFrom(h)
	for i, b := range hs.Bridges {
		if b.Bridge == st.Bridge && b.MAC == st.MAC {
			hs.Bridges = append(hs.Bridges[:i], hs.Bridges[i+1:]...)
			break
		}
	}
	return nil
}
