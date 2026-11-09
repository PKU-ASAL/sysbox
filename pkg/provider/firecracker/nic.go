package firecracker

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"strings"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

// AttachNIC creates a TAP device, connects it to the network's bridge,
// and records the NIC in the VM config JSON. The NIC will be available
// when StartNode launches the Firecracker process.
//
// Firecracker does NOT support NIC hot-plug in config-file mode,
// so we must declare all interfaces before boot.
type attachmentState struct {
	Fabric      *network.VMNamespace    `json:"fabric,omitempty"`
	Circuit     *network.VMCircuit      `json:"circuit,omitempty"`
	Link        *network.AttachmentLink `json:"link"`
	Tap         string                  `json:"tap"`
	NetNS       string                  `json:"netns"`
	GuestDevice string                  `json:"guest_device"`
}
type managedAttachment struct {
	Circuit     *network.VMCircuit     `json:"circuit,omitempty"`
	Name        string                 `json:"name"`
	GuestDevice string                 `json:"guest_device"`
	Target      network.BridgeTarget   `json:"target"`
	Link        network.AttachmentLink `json:"link"`
}
type networkState struct {
	ManagedBridge   *substrate.ManagedBridgeTarget `json:"managed_bridge"`
	NetNS           string                         `json:"netns"`
	Bridge          string                         `json:"bridge"`
	NAT             bool                           `json:"nat"`
	DockerNetworkID string                         `json:"docker_network_id"`
}
type linkRequest struct {
	Name, NetNS, IP, Gateway, MAC string
	Target                        network.BridgeTarget
}
type attachedNIC struct {
	Kind, HostEnd, GuestEnd, IP, NetNS string
	Circuit                            *network.VMCircuit
	Fabric                             *network.VMNamespace
	Link                               network.AttachmentLink
}

func attachmentTapName(nodeID, logicalName string) string {
	value := strings.TrimPrefix(nodeID, "sysbox-") + "-" + logicalName
	if len(value) <= 11 {
		return "tap-" + value
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return fmt.Sprintf("tap-%08x", h.Sum32())
}

func (s *Substrate) Attach(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest) (driver.AttachmentResult, error) {
	hs, ok := h.Provider.(*HandleState)
	if !ok || hs == nil || hs.ConfigPath == "" {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "VM config path missing", nil)
	}
	var target networkState
	if err := json.Unmarshal(req.NetworkState, &target); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "decode network state", err)
	}
	ip := ""
	if len(req.IPPrefixes) > 0 {
		ip = req.IPPrefixes[0]
	}
	bridgeTarget, err := attachmentBridgeTarget(req.NetworkIdentity, target)
	if err != nil {
		return driver.AttachmentResult{}, err
	}
	attached, err := s.attachNIC(ctx, h, linkRequest{Name: req.Name, NetNS: bridgeTarget.NetworkNamespace, Target: bridgeTarget, IP: ip, Gateway: req.Gateway, MAC: req.MAC})
	if err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorUnavailable, "firecracker", "attach network", err)
	}
	guest := attached.GuestEnd
	raw, _ := json.Marshal(attachmentState{Fabric: attached.Fabric, Circuit: attached.Circuit, Tap: attached.HostEnd, NetNS: attached.NetNS, GuestDevice: guest, Link: &attached.Link})
	return driver.AttachmentResult{Driver: "firecracker", GuestDevice: guest, State: raw}, nil
}
func (s *Substrate) Observe(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest, raw json.RawMessage) (driver.AttachmentResult, error) {
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "decode attachment state", err)
	}
	if st.Link == nil {
		return driver.AttachmentResult{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "attachment ownership proof required", nil)
	}
	_, m, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		return driver.AttachmentResult{}, err
	}
	defer release()
	id := substrate.OperationIdentity{ActionID: "attachment", OwnerUID: m.OwnerUID, ResourceUID: m.ResourceUID, TopologyUID: m.TopologyUID, Generation: m.Generation}
	alias, err := network.AttachmentAlias(id, req.Name)
	if err != nil {
		return driver.AttachmentResult{}, err
	}
	var target networkState
	if err := json.Unmarshal(req.NetworkState, &target); err != nil {
		return driver.AttachmentResult{}, err
	}
	bridgeTarget, err := attachmentBridgeTarget(req.NetworkIdentity, target)
	if err != nil {
		return driver.AttachmentResult{}, err
	}
	if st.Circuit != nil || st.Fabric != nil {
		if st.Circuit == nil || st.Fabric == nil || m.Handle.Fabric == nil {
			return driver.AttachmentResult{}, fmt.Errorf("incomplete VM circuit proof")
		}
		if err := st.Fabric.Validate(id); err != nil {
			return driver.AttachmentResult{}, err
		}
		if *st.Fabric != *m.Handle.Fabric || st.Link == nil || *st.Link != st.Circuit.Tap {
			return driver.AttachmentResult{}, fmt.Errorf("VM circuit state conflict")
		}
		found := false
		for _, a := range m.Handle.Attachments {
			if a.Name == req.Name && network.SameVMCircuit(a.Circuit, st.Circuit) {
				found = true
			}
		}
		if !found {
			return driver.AttachmentResult{}, fmt.Errorf("VM circuit absent from manifest")
		}
		bridge, err := network.OpenOwnedBridge(ctx, bridgeTarget)
		if err != nil {
			return driver.AttachmentResult{}, err
		}
		defer bridge.Close()
		if err = bridge.ObserveVMCircuit(ctx, *st.Fabric, *st.Circuit); err != nil {
			return driver.AttachmentResult{}, err
		}
		return driver.AttachmentResult{Driver: "firecracker", GuestDevice: st.GuestDevice, State: raw}, nil
	}
	if m.Handle.Fabric != nil {
		return driver.AttachmentResult{}, fmt.Errorf("VM circuit evidence missing")
	}
	bridge, err := network.OpenOwnedBridge(ctx, bridgeTarget)
	if err != nil {
		return driver.AttachmentResult{}, err
	}
	defer bridge.Close()
	if err := bridge.ObserveAttachment(ctx, *st.Link, alias); err != nil {
		return driver.AttachmentResult{}, err
	}
	return driver.AttachmentResult{Driver: "firecracker", GuestDevice: st.GuestDevice, State: raw}, nil
}

func attachmentBridgeTarget(identity substrate.OperationIdentity, target networkState) (network.BridgeTarget, error) {
	if target.NAT || target.DockerNetworkID != "" {
		if target.ManagedBridge == nil || target.ManagedBridge.NetworkID != target.DockerNetworkID {
			return network.BridgeTarget{}, driver.Wrap(driver.ErrorInvalidState, "firecracker", "managed bridge proof required; rebuild network with mixed-substrate consumers", nil)
		}
		return network.BridgeTarget{Identity: identity, Bridge: target.ManagedBridge.Bridge, Managed: target.ManagedBridge}, nil
	}
	return network.BridgeTarget{Identity: identity, NetworkNamespace: target.NetNS, Bridge: target.Bridge}, nil
}
func (s *Substrate) Delete(ctx context.Context, h substrate.NodeHandle, req driver.AttachmentRequest, raw json.RawMessage) error {
	var st attachmentState
	if err := json.Unmarshal(raw, &st); err != nil {
		return driver.Wrap(driver.ErrorInvalidState, "firecracker", "decode attachment state", err)
	}
	if st.Link == nil {
		return driver.Wrap(driver.ErrorInvalidState, "firecracker", "attachment ownership proof required", nil)
	}
	if st.Fabric != nil || st.Circuit != nil {
		hs, ok := h.Provider.(*HandleState)
		if !ok || hs.Fabric == nil || st.Fabric == nil || st.Circuit == nil || *hs.Fabric != *st.Fabric {
			return fmt.Errorf("VM fabric deletion proof missing")
		}
		if err := st.Circuit.Validate(*st.Fabric); err != nil {
			return err
		}
		// Reset invokes attachment deletion before DestroyReset; confirm the old
		// VMM stopped before disconnecting its fabric, using its canonical handle.
		if err := s.StopNode(ctx, h); err != nil {
			return err
		}
	}
	_, m, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		return err
	}
	defer release()
	id := substrate.OperationIdentity{ActionID: "attachment", OwnerUID: m.OwnerUID, ResourceUID: m.ResourceUID, TopologyUID: m.TopologyUID, Generation: m.Generation}
	alias, err := network.AttachmentAlias(id, req.Name)
	if err != nil {
		return err
	}
	if st.Circuit != nil || st.Fabric != nil {
		if st.Circuit == nil || st.Fabric == nil || m.Handle.Fabric == nil || *m.Handle.Fabric != *st.Fabric || st.Circuit.Tap != *st.Link {
			return fmt.Errorf("VM circuit deletion identity conflict")
		}
		if err := st.Fabric.Validate(id); err != nil {
			return err
		}
		found := false
		for _, a := range m.Handle.Attachments {
			if a.Name == req.Name && network.SameVMCircuit(a.Circuit, st.Circuit) {
				found = true
			}
		}
		if !found || st.Circuit.Alias != alias {
			return fmt.Errorf("VM circuit not owned by manifest")
		}
		live, _, err := probeProcess(m.Process)
		if err != nil {
			return err
		}
		if live {
			return fmt.Errorf("stop VMM before deleting circuit")
		}
		return network.DeleteVMCircuit(ctx, *st.Fabric, *st.Circuit)
	}
	if m.Handle.Fabric != nil {
		return fmt.Errorf("missing VM circuit deletion evidence")
	}
	return network.DeleteOwnedAttachment(ctx, *st.Link, alias)
}

func (s *Substrate) attachNIC(ctx context.Context, h substrate.NodeHandle, req linkRequest) (attachedNIC, error) {
	if req.NetNS == "" {
		return attachedNIC{}, fmt.Errorf("firecracker network attachments require a named network namespace")
	}
	vm, m, release, err := s.lockedManifest(ctx, h)
	if err != nil {
		return attachedNIC{}, err
	}
	defer release()
	if err := recoverLaunch(m); err != nil {
		return attachedNIC{}, err
	}
	live, _, err := probeProcess(m.Process)
	if err != nil {
		return attachedNIC{}, err
	}
	state := vm.snapshot()
	if live || state.destroying || state.destroyed {
		return attachedNIC{}, fmt.Errorf("firecracker NIC attachment requires a stopped, prepared VM")
	}
	hs := &m.Handle
	if hs.Fabric != nil || hs.NICCount == 0 {
		return s.attachFabricNIC(ctx, h, vm, m, req)
	}
	if hs.NICCount > 0 && hs.NetnsName != req.NetNS {
		return attachedNIC{}, fmt.Errorf("firecracker VM cannot attach NICs from multiple network namespaces: %q and %q", hs.NetnsName, req.NetNS)
	}
	if req.IP != "" {
		if _, _, err := splitCIDR(req.IP); err != nil {
			return attachedNIC{}, err
		}
	}
	// Parse the current configuration before any TAP or namespace mutation.
	raw, err := readRegularFile(hs.ConfigPath)
	if err != nil {
		return attachedNIC{}, err
	}
	var prepared fcConfig
	if err := json.Unmarshal(raw, &prepared); err != nil {
		return attachedNIC{}, err
	}
	bridge, err := network.OpenOwnedBridge(ctx, req.Target)
	if err != nil {
		return attachedNIC{}, err
	}
	defer bridge.Close()
	m.Phase = "attaching"
	if err := saveManifest(m); err != nil {
		return attachedNIC{}, err
	}

	tapName := attachmentTapName(h.ID, req.Name)
	id := substrate.OperationIdentity{ActionID: "attachment", OwnerUID: m.OwnerUID, ResourceUID: m.ResourceUID, TopologyUID: m.TopologyUID, Generation: m.Generation}
	alias, err := network.AttachmentAlias(id, req.Name)
	if err != nil {
		return attachedNIC{}, err
	}

	netnsName := req.NetNS
	if err := bridge.AttachTap(ctx, tapName, alias); err != nil {
		return attachedNIC{}, err
	}
	vm.mu.Lock()
	vm.netnsName = netnsName
	vm.mu.Unlock()
	hs.NetnsName = netnsName

	// Add NIC to the VM config JSON.
	cfgPath := hs.ConfigPath
	nicIdx := hs.NICCount
	for i, existing := range hs.Attachments {
		if existing.Name == req.Name {
			nicIdx = i
			break
		}
	}
	ifaceID := fmt.Sprintf("eth%d", nicIdx)
	fcIface := fcNetworkInterface{
		IfaceID:     ifaceID,
		HostDevName: tapName,
	}
	if req.MAC != "" {
		fcIface.GuestMAC = req.MAC
	}

	if err := appendNICtoConfig(cfgPath, fcIface); err != nil {
		return attachedNIC{}, fmt.Errorf("append NIC to config: %w", err)
	}

	// Phase A: kernel cmdline IP autoconfig for the first interface.
	if req.IP != "" {
		hostname := guestHostname(h.ID)
		if err := injectKernelIPArg(cfgPath, ifaceID, hostname, req.IP, req.Gateway); err != nil {
			return attachedNIC{}, fmt.Errorf("inject kernel ip= arg: %w", err)
		}
	}

	proof, err := bridge.CaptureAttachment(ctx, tapName, alias)
	if err != nil {
		return attachedNIC{}, err
	}
	attachment := managedAttachment{Name: req.Name, GuestDevice: ifaceID, Target: req.Target, Link: proof}
	if nicIdx < len(hs.Attachments) {
		hs.Attachments[nicIdx] = attachment
	} else {
		hs.Attachments = append(hs.Attachments, attachment)
	}
	hs.NICCount = len(hs.Attachments)
	hs.TapName = tapName
	m.Phase = "prepared"
	if err := refreshConfigDigest(m); err != nil {
		return attachedNIC{}, err
	}
	if err := saveManifest(m); err != nil {
		return attachedNIC{}, err
	}
	if original, _ := h.Provider.(*HandleState); original != nil {
		original.NICCount, original.TapName, original.NetnsName = hs.NICCount, hs.TapName, hs.NetnsName
		original.Attachments = append([]managedAttachment(nil), hs.Attachments...)
	}

	return attachedNIC{
		Link:     proof,
		Kind:     substrate.NICKindTap,
		HostEnd:  tapName,
		GuestEnd: ifaceID,
		IP:       req.IP,
		NetNS:    netnsName,
	}, nil
}

// injectKernelIPArg rewrites the VM config's boot_args to include a kernel
// IP-autoconfig directive for the given interface. Idempotent: if an ip=
// directive is already present, it is replaced.
//
// Format (kernel docs Documentation/admin-guide/nfs/nfsroot.rst):
//
//	ip=<client-ip>::<gw-ip>:<netmask>:<hostname>:<device>:<autoconf>
//
// We always set autoconf=off so the kernel does not try DHCP afterwards.
func injectKernelIPArg(cfgPath, dev, hostname, cidr, gw string) error {
	clientIP, mask, err := splitCIDR(cidr)
	if err != nil {
		return err
	}

	ipArg := fmt.Sprintf("ip=%s::%s:%s:%s:%s:off", clientIP, gw, mask, hostname, dev)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var cfg fcConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	cfg.BootSource.BootArgs = upsertCmdlineArg(cfg.BootSource.BootArgs, "ip", ipArg)

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return atomicWriteFile(cfgPath, out, 0600)
}

// upsertCmdlineArg replaces the first token whose key matches `key=` with the
// fully-formed `kv` token, or appends kv if no such token exists.
// Preserves order of other tokens.
//
// The kernel's ip= directive is repeatable — one entry per NIC, keyed by the
// trailing <dev> field — so for ip= we replace the entry for the SAME interface
// (idempotent re-apply) and append otherwise (multi-NIC nodes get one ip= per
// link). Other keys keep the single-token replace semantics.
func upsertCmdlineArg(cmdline, key, kv string) string {
	prefix := key + "="
	tokens := strings.Fields(cmdline)

	if key == "ip" {
		dev := ipArgDev(kv)
		out := make([]string, 0, len(tokens)+1)
		for _, t := range tokens {
			if strings.HasPrefix(t, prefix) && ipArgDev(t) == dev {
				continue // drop the previous entry for this interface
			}
			out = append(out, t)
		}
		out = append(out, kv)
		return strings.Join(out, " ")
	}

	replaced := false
	for i, t := range tokens {
		if strings.HasPrefix(t, prefix) {
			tokens[i] = kv
			replaced = true
			break
		}
	}
	if !replaced {
		tokens = append(tokens, kv)
	}
	return strings.Join(tokens, " ")
}

// ipArgDev returns the <dev> field of an
// ip=<client>::<gw>:<mask>:<host>:<dev>:<autoconf> token, or "" if the token
// cannot be parsed.
func ipArgDev(ipArg string) string {
	parts := strings.Split(ipArg, ":")
	if len(parts) >= 6 {
		return parts[5]
	}
	return ""
}

// splitCIDR splits "10.0.12.20/24" into ("10.0.12.20", "255.255.255.0").
func splitCIDR(cidr string) (string, string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", fmt.Errorf("parse cidr %s: %w", cidr, err)
	}
	mask := net.IP(ipnet.Mask).To4()
	if mask == nil {
		return "", "", fmt.Errorf("only IPv4 supported, got %s", cidr)
	}
	return ip.String(), mask.String(), nil
}

// appendNICtoConfig reads the VM config JSON, appends a network interface,
// and writes it back.
func appendNICtoConfig(cfgPath string, iface fcNetworkInterface) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg fcConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	replaced := false
	for i := range cfg.NetworkInterfaces {
		if cfg.NetworkInterfaces[i].IfaceID == iface.IfaceID {
			cfg.NetworkInterfaces[i] = iface
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.NetworkInterfaces = append(cfg.NetworkInterfaces, iface)
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return atomicWriteFile(cfgPath, out, 0600)
}

// Resolve at invocation, after the host bootstrap has installed its trusted PATH.
var ipBin = "ip"
