package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"

	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

// createRouter provisions a multi-NIC node with IP forwarding enabled.
// Interfaces on NAT (Docker-managed) networks are connected via Docker
// networking; isolated-network interfaces use veth pairs as usual.
// Optional NAT (nat_from -> nat_to) is delegated to the router network driver.
type RouterResourceHandler struct{}

func init() {
	RegisterResourceHandler(RouterResourceHandler{})
}

func (RouterResourceHandler) Type() string { return "sysbox_router" }

func (RouterResourceHandler) Schema() ResourceSchema {
	return ResourceSchemaFor("sysbox_router")
}

func (RouterResourceHandler) Read(ctx context.Context, current state.Resource) (ResourceReadResult, error) {
	result, err := readNodeLikeResource(ctx, current)
	if err != nil || result.Status != state.ResourcePresent || current.Str("policy_owner") == "" {
		return result, err
	}
	policy, err := driver.DefaultRegistry.RequirePolicy(current.Driver)
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown, Resource: current, Reason: err.Error()}, err
	}
	observation, err := policy.ObserveRuleset(ctx, driver.PolicyTarget{Resource: current.Address.String(), State: json.RawMessage(current.Str("policy_target_state"))}, current.Str("policy_owner"))
	if err != nil {
		if driver.IsCategory(err, driver.ErrorNotFound) {
			return ResourceReadResult{Status: state.ResourceDrifted, Resource: current, Reason: "router policy table not found"}, nil
		}
		return ResourceReadResult{Status: state.ResourceUnknown, Resource: current, Reason: err.Error()}, err
	}
	if observation.Digest != current.Str("policy_digest") {
		return ResourceReadResult{Status: state.ResourceDrifted, Resource: current, Reason: "router policy digest mismatch"}, nil
	}
	return result, nil
}

func (RouterResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (controlplane.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}

func (RouterResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.createRouterResource(ctx, n)
}

func (RouterResourceHandler) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	if owner := current.Str("policy_owner"); owner != "" {
		policy, err := driver.DefaultRegistry.RequirePolicy(current.Driver)
		if err != nil {
			return err
		}
		target := driver.PolicyTarget{Resource: current.Address.String(), State: json.RawMessage(current.Str("policy_target_state"))}
		if err := policy.DeleteRuleset(ctx, target, owner); err != nil && !driver.IsCategory(err, driver.ErrorNotFound) {
			return fmt.Errorf("delete router %s policy: %w", current.Address, err)
		}
	}
	return pc.destroyNodeResource(ctx, current)
}

func (RouterResourceHandler) ExternalID(current state.Resource) string {
	if id := current.ContainerID(); id != "" {
		return id
	}
	return current.Str("id")
}
func (RouterResourceHandler) RequiredCapabilities(node *graph.Node) ([]CapabilityRequirement, error) {
	cfg, ok := node.Data.(*config.RouterConfig)
	if !ok {
		return nil, nil
	}
	name, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return nil, err
	}
	required := []CapabilityRequirement{{name, driver.CapabilityNode}, {name, driver.CapabilityNIC}, {name, driver.CapabilityNodeState}}
	if cfg.NatFrom != "" || cfg.NatTo != "" {
		required = append(required, CapabilityRequirement{name, driver.CapabilityPolicy})
	}
	return required, nil
}

func (RouterResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext) (any, []address.Address, error) {
	cfg := &config.RouterConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	var deps []address.Address
	if cfg.Image != "" {
		ref, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
		if err != nil {
			return nil, nil, err
		}
		deps = append(deps, ref)
	}
	for _, iface := range cfg.Interfaces {
		if iface.Network != "" {
			ref, err := config.ResolveResourceAddress(iface.Network, "sysbox_network")
			if err != nil {
				return nil, nil, err
			}
			deps = append(deps, ref)
		}
	}
	return cfg, deps, nil
}

func (e *Executor) createRouterResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	cfg, ok := n.Data.(*config.RouterConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("router %s: wrong data type", n.Address)
	}
	subName, err := resolveSubstrateRef(cfg.Substrate)
	if err != nil {
		return state.Resource{}, err
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(subName)
	if err != nil {
		return state.Resource{}, err
	}
	nicDriver, err := driver.DefaultRegistry.RequireNIC(subName)
	if err != nil {
		return state.Resource{}, err
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(subName)
	if err != nil {
		return state.Resource{}, err
	}

	imageAddr, err := config.ResolveResourceAddress(cfg.Image, "sysbox_image")
	if err != nil {
		return state.Resource{}, err
	}
	imgState := e.state.FindResource(imageAddr)
	if imgState == nil {
		return state.Resource{}, fmt.Errorf("image %s not applied yet", imageAddr)
	}
	imgRef := substrate.ImageRef{
		ID:         imgState.ImageID(),
		Repository: imgState.Repository(),
	}

	inputs := make([]AttachmentInput, 0, len(cfg.Interfaces))
	for _, iface := range cfg.Interfaces {
		inputs = append(inputs, AttachmentInput{
			Name: iface.Name, Network: iface.Network, IPPrefixes: []string{iface.IP},
		})
	}
	intents, err := NormalizeAttachmentIntents(e.topology, n.Address, inputs)
	if err != nil {
		return state.Resource{}, err
	}
	nicSpecs := nicSpecsFromAttachmentIntents(intents)
	hasManagedNetwork := false
	for _, spec := range nicSpecs {
		netAddr, resolveErr := config.ResolveResourceAddress(spec.Network, "sysbox_network")
		if resolveErr != nil {
			return state.Resource{}, resolveErr
		}
		if network := e.state.FindResource(netAddr); network != nil && network.IsNAT() {
			hasManagedNetwork = true
			break
		}
	}

	handle, err := nodeDriver.CreateNode(ctx, substrate.NodeSpec{
		Name:           runtimeExternalName(e.topology, "router", n.Address.Name),
		Image:          imgRef,
		Sysctls:        map[string]string{"net.ipv4.ip_forward": "1"},
		Labels:         ManagedLabels(e.topology, e.runID, n.Address),
		ManagedNetwork: hasManagedNetwork,
	})
	if err != nil {
		return state.Resource{}, err
	}

	caps := nodeDriver.Capabilities()
	if caps.NICHotPlug {
		if err := nodeDriver.StartNode(ctx, handle); err != nil {
			util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy router on start failure")
			return state.Resource{}, err
		}
	}

	// Wire all NICs using the shared helper (trackLabels=true for routers).
	wireResult, err := wireNICs(ctx, nicDriver, e.state, handle, nicSpecs, n.Address)
	if err != nil {
		util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy router on wire failure")
		return state.Resource{}, err
	}
	if !caps.NICHotPlug {
		if err := nodeDriver.StartNode(ctx, handle); err != nil {
			util.BestEffortIgnore(func() error { return nodeDriver.DestroyNode(ctx, handle) }, "destroy router on cold-start failure")
			return state.Resource{}, err
		}
	}

	natApplied := false
	var policyOwner, policyTable, policyDigest, policyTargetState, policySpec string
	if cfg.NatFrom != "" && cfg.NatTo != "" {
		fromReq, ok1 := wireResult.Requests[cfg.NatFrom]
		_, ok2 := wireResult.Requests[cfg.NatTo]
		if !ok1 || !ok2 {
			return state.Resource{}, fmt.Errorf("nat_from %q / nat_to %q must reference declared interfaces",
				cfg.NatFrom, cfg.NatTo)
		}
		policy, err := driver.DefaultRegistry.RequirePolicy(subName)
		if err != nil {
			return state.Resource{}, err
		}
		bindings := map[string]string{}
		attachmentIPs := map[string][]string{}
		for name, result := range wireResult.Results {
			bindings[name] = result.GuestDevice
			attachmentIPs[name] = append([]string(nil), wireResult.Requests[name].IPPrefixes...)
		}
		targetRaw, err := json.Marshal(map[string]any{"container_id": handle.ID, "bindings": bindings, "attachment_ips": attachmentIPs})
		if err != nil {
			return state.Resource{}, err
		}
		policyOwner = e.topology + "/" + n.Address.String()
		spec := driver.RulesetSpec{Owner: policyOwner, Family: driver.FamilyIPv4, DefaultInput: driver.VerdictAccept, DefaultOutput: driver.VerdictAccept, DefaultForward: driver.VerdictDrop,
			Rules: []driver.PolicyRule{
				{ID: "nat-forward", Direction: driver.DirectionForward, InputAttachment: cfg.NatFrom, OutputAttachment: cfg.NatTo, Protocol: driver.ProtocolAll, Verdict: driver.VerdictAccept, Counter: true},
				{ID: "nat-return", Direction: driver.DirectionForward, InputAttachment: cfg.NatTo, OutputAttachment: cfg.NatFrom, Protocol: driver.ProtocolAll, States: []driver.ConnectionState{driver.StateEstablished, driver.StateRelated}, Verdict: driver.VerdictAccept, Counter: true},
			}, NAT: &driver.NATPolicy{SourceAttachment: cfg.NatFrom, UplinkAttachment: cfg.NatTo, SourceCIDRs: append([]string(nil), fromReq.IPPrefixes...), Masquerade: true}}
		observation, err := policy.ApplyRuleset(ctx, driver.PolicyTarget{Resource: n.Address.String(), State: targetRaw}, spec)
		if err != nil {
			return state.Resource{}, fmt.Errorf("router %s NAT policy: %w", n.Address.Name, err)
		}
		specRaw, err := json.Marshal(spec)
		if err != nil {
			return state.Resource{}, err
		}
		natApplied, policyTable, policyDigest, policyTargetState = true, observation.Table, observation.Digest, string(targetRaw)
		policySpec = string(specRaw)
	}

	inst := map[string]any{
		"container_id":        handle.ID,
		"primary_ip":          wireResult.PrimaryIP,
		"nat_applied":         natApplied,
		"policy_owner":        policyOwner,
		"policy_table":        policyTable,
		"policy_digest":       policyDigest,
		"policy_target_state": policyTargetState,
		"policy_spec":         policySpec,
	}
	// Persist opaque provider state so cold-destroy works for all substrates.
	blob, _ := stateDriver.MarshalProviderState(handle)
	// Persist lifecycle flags.
	if lc := cfg.Lifecycle; lc != nil {
		inst["lifecycle_prevent_destroy"] = lc.PreventDestroy
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	resource := state.Resource{
		Address:     n.Address,
		Driver:      subName,
		Attributes:  state.MustAttributes(inst),
		Attachments: wireResult.Attachments,
	}
	if len(blob) > 0 {
		_ = resource.SetProviderState(blob)
	}
	return resource, nil
}
