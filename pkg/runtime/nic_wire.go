package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

// NICSpec is the substrate-neutral description of a single network attachment
// request. Both NodeConfig.Links and RouterConfig.Interfaces are mapped onto
// this before wiring, so the shared wiring loop doesn't depend on config types.
type NICSpec struct {
	Name    string
	Network string // resolved resource name
	IP      string
	Gateway string // empty for router interfaces
	MAC     string
}

// NICWireResult holds the outputs of wireNICs that both node and router
// creation need: the per-NIC state entries and the name→guest-iface mapping
// (used by router for nat_from/nat_to resolution).
type NICWireResult struct {
	Attachments []state.Attachment
	Requests    map[string]driver.AttachmentRequest
	Results     map[string]driver.AttachmentResult
	PrimaryIP   string
}

type NICWireHook func(phase string, details map[string]any, fn func() error) error

// wireNICs applies normalized logical attachments through the owning driver.
func wireNICs(ctx context.Context, nicDriver driver.NIC, st *state.State,
	handle substrate.NodeHandle, specs []NICSpec, owner address.Address,
) (*NICWireResult, error) {
	return wireNICsWithHook(ctx, nicDriver, st, handle, specs, owner, nil)
}

func wireNICsWithHook(ctx context.Context, nicDriver driver.NIC, st *state.State,
	handle substrate.NodeHandle, specs []NICSpec, owner address.Address, hook NICWireHook,
) (*NICWireResult, error) {
	result := &NICWireResult{Requests: map[string]driver.AttachmentRequest{}, Results: map[string]driver.AttachmentResult{}}

	for _, spec := range specs {
		netAddr, err := config.ResolveResourceAddress(spec.Network, "sysbox_network")
		if err != nil {
			return result, err
		}
		netState := st.FindResource(netAddr)
		if netState == nil {
			return nil, fmt.Errorf("network %s not applied yet", spec.Network)
		}

		networkState, err := networkAttachmentState(*netState)
		if err != nil {
			return nil, err
		}
		request := driver.AttachmentRequest{Name: spec.Name, Network: netAddr, MAC: spec.MAC, IPPrefixes: []string{spec.IP}, Gateway: spec.Gateway, NetworkState: networkState}
		var attached driver.AttachmentResult
		if err := runNICWireHook(hook, "attach", map[string]any{
			"node":    owner.String(),
			"network": spec.Network,
			"name":    spec.Name,
			"ip":      spec.IP,
		}, func() error {
			var err error
			attached, err = nicDriver.Attach(ctx, handle, request)
			return err
		}); err != nil {
			return nil, err
		}
		result.Requests[spec.Name] = request
		result.Results[spec.Name] = attached
		result.Attachments = append(result.Attachments, state.Attachment{Name: spec.Name, Node: owner, Network: netAddr, MAC: spec.MAC, IPPrefixes: []string{spec.IP}, Gateway: spec.Gateway, Driver: attached.Driver, Observation: state.AttachmentObservation{GuestDevice: attached.GuestDevice}, DriverState: attached.State})

		if result.PrimaryIP == "" && spec.IP != "" {
			result.PrimaryIP = stripCIDR(spec.IP)
		}
	}

	return result, nil
}

func runNICWireHook(hook NICWireHook, phase string, details map[string]any, fn func() error) error {
	if hook == nil {
		return fn()
	}
	return hook(phase, details, fn)
}

func attachmentRequestFromState(st *state.State, attachment state.Attachment) (driver.AttachmentRequest, error) {
	network := st.FindResource(attachment.Network)
	if network == nil {
		return driver.AttachmentRequest{}, fmt.Errorf("network %s not found for attachment %s", attachment.Network, attachment.Name)
	}
	raw, err := networkAttachmentState(*network)
	if err != nil {
		return driver.AttachmentRequest{}, err
	}
	return driver.AttachmentRequest{Name: attachment.Name, Network: attachment.Network, MAC: attachment.MAC,
		IPPrefixes: append([]string(nil), attachment.IPPrefixes...), Gateway: attachment.Gateway, NetworkState: raw}, nil
}

func networkAttachmentState(network state.Resource) (json.RawMessage, error) {
	projection := make(map[string]any, len(network.AttributeMap()))
	for key, value := range network.AttributeMap() {
		projection[key] = value
	}
	runtimeState, err := network.RuntimeState()
	if err != nil {
		return nil, err
	}
	for key, value := range runtimeState {
		projection[key] = value
	}
	return json.Marshal(projection)
}

// stripCIDR removes the /NN suffix from an IP/CIDR string.
func stripCIDR(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i]
		}
	}
	return s
}
