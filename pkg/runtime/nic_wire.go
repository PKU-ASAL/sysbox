package runtime

import (
	"context"
	"fmt"

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
	NICs        []map[string]any
	IfaceByName map[string]string // label → guest iface (e.g. "lan" → "eth1")
	PrimaryIP   string            // first non-empty IP
}

type NICWireHook func(phase string, details map[string]any, fn func() error) error

// collectNATLinks pre-scans specs to find Docker-NAT networks.
// When allNAT is true, every NAT network is returned as an InitialLink
// (used by node creation which can attach multiple NAT nets at create time).
// When allNAT is false, only the first NAT network is returned
// (used by router creation which attaches the first at create time and
// the rest post-start via AttachNIC, to control eth naming).
func collectNATLinks(st *state.State, specs []NICSpec, allNAT bool) ([]substrate.LinkRequest, error) {
	var initial []substrate.LinkRequest
	for _, spec := range specs {
		netAddr, err := config.ResolveResourceAddress(spec.Network, "sysbox_network")
		if err != nil {
			return nil, err
		}
		netState := st.FindResource(netAddr)
		if netState == nil {
			return nil, fmt.Errorf("network %s not applied yet", spec.Network)
		}
		if netState.IsNAT() {
			if allNAT || len(initial) == 0 {
				initial = append(initial, substrate.LinkRequest{
					KindHint:    substrate.NICKindDockerNAT,
					DockerNetID: netState.DockerNetID(),
					IP:          spec.IP,
					MAC:         spec.MAC,
				})
			}
		}
	}
	return initial, nil
}

// wireNICs attaches all network interfaces to a node that has already been
// created (and possibly started). It handles:
//   - NAT networks already connected at create-time (skipped),
//   - Extra NAT networks attached via docker network connect,
//   - Isolated (non-NAT) networks attached via substrate.AttachNIC.
//
// Parameters:
//   - ctx: cancellation context
//   - sub: substrate to call AttachNIC on
//   - st: state for looking up network resources
//   - handle: the created node handle (for AttachNIC calls)
//   - initialLinks: the InitialLinks that were passed to CreateNode (so we
//     can skip re-attaching the first NAT network)
//   - specs: the NICSpec list (one per link/interface)
//   - trackLabels: when true, populates IfaceByName and adds "label"/"kind"
//     keys to NIC entries (needed by router for nat_from/nat_to)
//   - nodeName: used in error messages
func wireNICs(ctx context.Context, nicDriver driver.NIC, st *state.State,
	handle substrate.NodeHandle, initialLinks []substrate.LinkRequest,
	specs []NICSpec, trackLabels bool, nodeName string,
) (*NICWireResult, error) {
	return wireNICsWithHook(ctx, nicDriver, st, handle, initialLinks, specs, trackLabels, nodeName, nil)
}

func wireNICsWithHook(ctx context.Context, nicDriver driver.NIC, st *state.State,
	handle substrate.NodeHandle, initialLinks []substrate.LinkRequest,
	specs []NICSpec, trackLabels bool, nodeName string, hook NICWireHook,
) (*NICWireResult, error) {

	connectedAtCreate := map[string]bool{}
	for _, il := range initialLinks {
		if il.DockerNetID != "" {
			connectedAtCreate[il.DockerNetID] = true
		}
	}

	natIdx := 0                  // NAT ifaces numbered eth0, eth1, ... by Docker
	vethIdx := len(initialLinks) // veth guest-iface starts after NAT ifaces

	result := &NICWireResult{
		IfaceByName: map[string]string{},
	}

	for _, spec := range specs {
		netAddr, err := config.ResolveResourceAddress(spec.Network, "sysbox_network")
		if err != nil {
			return result, err
		}
		netState := st.FindResource(netAddr)
		if netState == nil {
			return nil, fmt.Errorf("network %s not applied yet", spec.Network)
		}

		if netState.IsNAT() {
			netID := netState.DockerNetID()
			if !connectedAtCreate[netID] {
				attach := func() error {
					_, err := nicDriver.AttachNIC(ctx, handle, substrate.LinkRequest{
						KindHint:    substrate.NICKindDockerNAT,
						DockerNetID: netID,
						IP:          spec.IP,
						MAC:         spec.MAC,
					})
					return err
				}
				if err := runNICWireHook(hook, "attach_nat_network", map[string]any{
					"node":       nodeName,
					"network":    spec.Network,
					"network_id": netID,
					"ip":         spec.IP,
				}, attach); err != nil {
					return nil, fmt.Errorf("connect %s to nat network %s: %w", nodeName, spec.Network, err)
				}
			}

			entry := map[string]any{
				"kind":       substrate.NICKindDockerNAT,
				"network_id": netID,
				"ip":         spec.IP,
			}
			// Router-mode: track eth naming and labels for nat_from/nat_to.
			if trackLabels {
				target := fmt.Sprintf("eth%d", natIdx)
				natIdx++
				entry["target"] = target
				entry["label"] = spec.Name
				if spec.Name != "" {
					result.IfaceByName[spec.Name] = target
				}
			}
			result.NICs = append(result.NICs, entry)

			// Record primary IP from first link that has one.
			if result.PrimaryIP == "" && spec.IP != "" {
				// Strip CIDR suffix for PrimaryIP.
				result.PrimaryIP = stripCIDR(spec.IP)
			}
			continue
		}

		// Non-NAT (isolated) network: delegate NIC creation to the substrate.
		lreq := substrate.LinkRequest{
			NetNS:      netState.Str("netns"),
			Bridge:     netState.Str("bridge"),
			IP:         spec.IP,
			Gateway:    spec.Gateway,
			MAC:        spec.MAC,
			TargetName: fmt.Sprintf("eth%d", vethIdx),
		}
		var attached substrate.AttachedNIC
		if err := runNICWireHook(hook, "attach_nic", map[string]any{
			"node":    nodeName,
			"network": spec.Network,
			"netns":   lreq.NetNS,
			"bridge":  lreq.Bridge,
			"ip":      lreq.IP,
			"target":  lreq.TargetName,
		}, func() error {
			var err error
			attached, err = nicDriver.AttachNIC(ctx, handle, lreq)
			return err
		}); err != nil {
			return nil, err
		}
		vethIdx++

		entry := map[string]any{
			"kind":      attached.Kind,
			"host_end":  attached.HostEnd,
			"guest_end": attached.GuestEnd,
			"target":    lreq.TargetName,
			"ip":        attached.IP,
			"netns":     attached.NetNS,
		}
		if trackLabels {
			entry["label"] = spec.Name
			if spec.Name != "" {
				result.IfaceByName[spec.Name] = lreq.TargetName
			}
		}
		result.NICs = append(result.NICs, entry)

		if result.PrimaryIP == "" && attached.IP != "" {
			result.PrimaryIP = stripCIDR(attached.IP)
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

// stripCIDR removes the /NN suffix from an IP/CIDR string.
func stripCIDR(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i]
		}
	}
	return s
}
