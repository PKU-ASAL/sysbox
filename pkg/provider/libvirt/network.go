package libvirt

import (
	"context"
	"encoding/json"

	"github.com/oslab/sysbox/pkg/driver"
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
	Netns         string `json:"netns"`
	Bridge        string `json:"bridge"`
	LibvirtBridge string `json:"libvirt_bridge"`
}

func (s *Substrate) Attach(_ context.Context, h substrate.NodeHandle, req driver.AttachmentRequest) (driver.AttachmentResult, error) {
	var target networkState
	if err := json.Unmarshal(req.NetworkState, &target); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "libvirt", "decode network state", err)
	}
	hs := hsFrom(h)
	bridge := target.LibvirtBridge
	if bridge == "" {
		bridge = target.Bridge
	}
	hs.Bridges = append(hs.Bridges, BridgeAttach{Name: req.Name, Netns: target.Netns, Bridge: bridge, MAC: req.MAC, IPPrefixes: append([]string(nil), req.IPPrefixes...), Gateway: req.Gateway})
	raw, _ := json.Marshal(attachmentState{Bridge: bridge, MAC: req.MAC})
	return driver.AttachmentResult{Driver: "libvirt", State: raw}, nil
}
func (s *Substrate) Observe(_ context.Context, h substrate.NodeHandle, _ driver.AttachmentRequest, raw json.RawMessage) (driver.AttachmentResult, error) {
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "libvirt", "decode attachment state", err)
	}
	found := false
	for _, bridge := range hsFrom(h).Bridges {
		if bridge.Bridge == st.Bridge && bridge.MAC == st.MAC {
			found = true
			break
		}
	}
	if !found {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorNotFound, "libvirt", "attachment bridge not found", nil)
	}
	return driver.AttachmentResult{Driver: "libvirt", State: raw}, nil
}
func (s *Substrate) Delete(_ context.Context, h substrate.NodeHandle, _ driver.AttachmentRequest, raw json.RawMessage) error {
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
