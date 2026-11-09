package firecracker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/substrate"
)

func manifestNetworkIdentity(m *vmManifest) substrate.OperationIdentity {
	return substrate.OperationIdentity{ActionID: "attachment", TopologyUID: m.TopologyUID, OwnerUID: m.OwnerUID, ResourceUID: m.ResourceUID, Generation: m.Generation}
}
func validateFabricHandle(h HandleState, id substrate.OperationIdentity) error {
	if h.FabricPending != "" && h.FabricPending != network.VMNamespaceName(id) {
		return fmt.Errorf("VM namespace intent identity mismatch")
	}
	if h.Fabric == nil {
		for _, a := range h.Attachments {
			if a.Circuit != nil {
				return fmt.Errorf("VM fabric namespace proof missing")
			}
		}
		return nil
	}
	if err := h.Fabric.Validate(id); err != nil {
		return err
	}
	if h.NetnsName != h.Fabric.Name {
		return fmt.Errorf("VM launch namespace differs from fabric")
	}
	names := map[string]bool{}
	for _, a := range h.Attachments {
		if names[a.Name] || a.Circuit == nil {
			return fmt.Errorf("VM fabric attachment proof missing or duplicated")
		}
		names[a.Name] = true
		alias, err := network.AttachmentAlias(id, a.Name)
		if err != nil {
			return err
		}
		if err = a.Circuit.Validate(*h.Fabric); err != nil {
			return err
		}
		if a.Circuit.Alias != alias || a.Link != a.Circuit.Tap || a.Target.NetworkNamespace != a.Circuit.Remote.Namespace || a.Target.Managed != nil || a.Target.Root {
			return fmt.Errorf("VM fabric ownership mismatch")
		}
	}
	return nil
}
func (s *Substrate) attachFabricNIC(ctx context.Context, h substrate.NodeHandle, vm *vmProcess, m *vmManifest, req linkRequest) (attachedNIC, error) {
	hs := &m.Handle
	if req.Target.Managed != nil || req.Target.Root || req.NetNS == "" || req.NetNS != req.Target.NetworkNamespace {
		return attachedNIC{}, fmt.Errorf("VM fabric requires an isolated named network")
	}
	if req.IP != "" {
		if _, _, err := splitCIDR(req.IP); err != nil {
			return attachedNIC{}, err
		}
	}
	if hs.NICCount >= 8 {
		return attachedNIC{}, fmt.Errorf("Firecracker supports at most 8 interfaces")
	}
	for _, a := range hs.Attachments {
		if a.Name == req.Name {
			return attachedNIC{}, fmt.Errorf("attachment already exists; observe original intent")
		}
	}
	id := manifestNetworkIdentity(m)
	if err := validateFabricHandle(*hs, id); err != nil {
		return attachedNIC{}, err
	}
	raw, err := readRegularFile(hs.ConfigPath)
	if err != nil {
		return attachedNIC{}, err
	}
	var prepared fcConfig
	if err = json.Unmarshal(raw, &prepared); err != nil {
		return attachedNIC{}, err
	}
	if len(prepared.NetworkInterfaces) != hs.NICCount {
		return attachedNIC{}, fmt.Errorf("VM config inventory mismatch")
	}
	// Verify target before creating a VM namespace. Release before exclusive
	// namespace creation; the same target is pinned again for link construction.
	target, err := network.OpenOwnedBridge(ctx, req.Target)
	if err != nil {
		return attachedNIC{}, err
	}
	target.Close()
	if hs.Fabric == nil {
		hs.FabricPending = network.VMNamespaceName(id)
	}
	m.Phase = "attaching"
	if err = saveManifest(m); err != nil {
		return attachedNIC{}, err
	}
	if hs.Fabric == nil {
		ns, err := network.NewVMNamespace(ctx, id)
		if err != nil {
			return attachedNIC{}, err
		}
		hs.Fabric = &ns
		hs.FabricPending = ""
		hs.NetnsName = ns.Name
		if err = saveManifest(m); err != nil {
			return attachedNIC{}, err
		}
		if err = network.ConfigureVMNamespace(ctx, ns); err != nil {
			return attachedNIC{}, err
		}
	}
	bridge, err := network.OpenOwnedBridge(ctx, req.Target)
	if err != nil {
		return attachedNIC{}, err
	}
	defer bridge.Close()
	alias, err := network.AttachmentAlias(id, req.Name)
	if err != nil {
		return attachedNIC{}, err
	}
	circuit, err := bridge.PrepareVMCircuit(ctx, *hs.Fabric, alias)
	if err != nil {
		return attachedNIC{}, err
	}
	index := len(hs.Attachments)
	iface := fmt.Sprintf("eth%d", index)
	hs.Attachments = append(hs.Attachments, managedAttachment{Name: req.Name, GuestDevice: iface, Target: req.Target, Link: circuit.Tap, Circuit: &circuit})
	if err = saveManifest(m); err != nil {
		return attachedNIC{}, err
	}
	checkpoint := func(next network.VMCircuit) error {
		copy := next
		hs.Attachments[index].Circuit = &copy
		return saveManifest(m)
	}
	if err = bridge.CreateVMCircuit(ctx, *hs.Fabric, &circuit, checkpoint); err != nil {
		return attachedNIC{}, err
	}
	if err = appendNICtoConfig(hs.ConfigPath, fcNetworkInterface{IfaceID: iface, HostDevName: circuit.Tap.Name, GuestMAC: req.MAC}); err != nil {
		return attachedNIC{}, err
	}
	if req.IP != "" {
		if err = injectKernelIPArg(hs.ConfigPath, iface, guestHostname(h.ID), req.IP, req.Gateway); err != nil {
			return attachedNIC{}, err
		}
	}
	hs.NICCount = len(hs.Attachments)
	hs.TapName = circuit.Tap.Name
	m.Phase = "prepared"
	if err = refreshConfigDigest(m); err != nil {
		return attachedNIC{}, err
	}
	if err = saveManifest(m); err != nil {
		return attachedNIC{}, err
	}
	vm.mu.Lock()
	vm.netnsName = hs.NetnsName
	vm.mu.Unlock()
	if original, ok := h.Provider.(*HandleState); ok {
		*original = *cloneHandleState(hs)
	}
	return attachedNIC{Circuit: &circuit, Fabric: hs.Fabric, Kind: substrate.NICKindTap, HostEnd: circuit.Tap.Name, GuestEnd: iface, IP: req.IP, NetNS: hs.NetnsName, Link: circuit.Tap}, nil
}
func cleanupFabric(ctx context.Context, m *vmManifest) error {
	if m.Handle.FabricPending != "" {
		return fmt.Errorf("VM namespace creation outcome unknown")
	}
	if m.Handle.Fabric == nil {
		return nil
	}
	if err := validateFabricHandle(m.Handle, manifestNetworkIdentity(m)); err != nil {
		return err
	}
	for _, a := range m.Handle.Attachments {
		if err := network.DeleteVMCircuit(ctx, *m.Handle.Fabric, *a.Circuit); err != nil {
			return err
		}
	}
	return network.DeleteVMNamespace(ctx, *m.Handle.Fabric)
}

func supportedNetworkHandle(h HandleState) bool {
	return h.Version == manifestVersion || (h.Version == 3 && h.Fabric == nil && h.FabricPending == "")
}
