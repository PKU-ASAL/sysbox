package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type FirewallResourceHandler struct{}

func init()                                            { RegisterResourceHandler(FirewallResourceHandler{}) }
func (FirewallResourceHandler) Type() string           { return "sysbox_firewall" }
func (FirewallResourceHandler) Schema() ResourceSchema { return ResourceSchemaFor("sysbox_firewall") }

func (FirewallResourceHandler) Read(ctx context.Context, current state.Resource, providers *driver.Registry) (ResourceReadResult, error) {
	policy, err := providers.RequirePolicy(current.Driver)
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown, Resource: current, Reason: err.Error()}, err
	}
	target, owner, err := policyState(current)
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown, Resource: current, Reason: err.Error()}, err
	}
	observation, err := policy.ObserveRuleset(ctx, target, owner)
	if err != nil {
		if driver.IsCategory(err, driver.ErrorNotFound) {
			return ResourceReadResult{Status: state.ResourceAbsent, Resource: current, Reason: err.Error()}, nil
		}
		return ResourceReadResult{Status: state.ResourceUnknown, Resource: current, Reason: err.Error()}, err
	}
	if observation.Digest != current.Str("desired_digest") {
		return ResourceReadResult{Status: state.ResourceDrifted, Resource: current, Reason: "firewall ruleset digest mismatch"}, nil
	}
	return resourceReadOK(current), nil
}

func (FirewallResourceHandler) PlanDiff(desired *graph.Node, current *state.Resource) (model.PlannedChange, error) {
	return planDiffByDesiredHash(desired, current)
}
func (FirewallResourceHandler) Create(ctx context.Context, pc *ProviderContext, n *graph.Node) (state.Resource, error) {
	return pc.createFirewallResource(ctx, n)
}

func (FirewallResourceHandler) Delete(ctx context.Context, pc *ProviderContext, current state.Resource) error {
	policy, err := pc.exec.providers.RequirePolicy(current.Driver)
	if err != nil {
		return err
	}
	target, owner, err := policyState(current)
	if err != nil {
		return err
	}
	if err := policy.DeleteRuleset(ctx, target, owner); err != nil {
		return fmt.Errorf("delete firewall %s: %w", current.Address, err)
	}
	pc.State().RemoveResource(current.Address)
	return nil
}
func (FirewallResourceHandler) ExternalID(current state.Resource) string { return current.Str("table") }

func (FirewallResourceHandler) DecodeResource(r config.ResourceBlock, _ string, ctx *hcl.EvalContext, providers *driver.Registry) (any, []address.Address, error) {
	cfg := &config.FirewallConfig{}
	if err := config.DecodeResource(&r, cfg, ctx); err != nil {
		return nil, nil, err
	}
	target, err := resolvePolicyTargetAddress(cfg.AttachTo)
	if err != nil {
		return nil, nil, err
	}
	if _, err := firewallRuleset("validation/sysbox_firewall."+r.Name, cfg); err != nil {
		return nil, nil, err
	}
	return cfg, []address.Address{target}, nil
}

func (e *Executor) createFirewallResource(ctx context.Context, n *graph.Node) (state.Resource, error) {
	return e.resolveFirewallResource(ctx, n, false)
}

func (FirewallResourceHandler) Discover(ctx context.Context, pc *ProviderContext, n *graph.Node) (ResourceReadResult, error) {
	r, err := pc.exec.resolveFirewallResource(ctx, n, true)
	if driver.IsCategory(err, driver.ErrorNotFound) {
		return ResourceReadResult{Status: state.ResourceAbsent}, nil
	}
	if err != nil {
		return ResourceReadResult{Status: state.ResourceUnknown, Reason: err.Error()}, err
	}
	return resourceReadOK(r), nil
}

func (e *Executor) resolveFirewallResource(ctx context.Context, n *graph.Node, discover bool) (state.Resource, error) {
	cfg, ok := n.Data.(*config.FirewallConfig)
	if !ok {
		return state.Resource{}, fmt.Errorf("firewall %s: wrong data type", n.Address)
	}
	targetAddr, err := resolvePolicyTargetAddress(cfg.AttachTo)
	if err != nil {
		return state.Resource{}, err
	}
	targetResource := e.state.FindResource(targetAddr)
	if targetResource == nil {
		return state.Resource{}, fmt.Errorf("firewall %s: target %s not applied yet", n.Address.Name, targetAddr)
	}
	bindings := map[string]string{}
	attachmentIPs := map[string][]string{}
	for _, attachment := range targetResource.Attachments {
		bindings[attachment.Name] = attachment.Observation.GuestDevice
		attachmentIPs[attachment.Name] = append([]string(nil), attachment.IPPrefixes...)
	}
	targetIdentity := substrate.OperationIdentity{ActionID: "policy-target", OwnerUID: targetResource.OwnerUID, TopologyUID: targetResource.OwnerUID, ResourceUID: targetResource.ResourceUID, Generation: targetResource.Generation}
	if err := targetIdentity.Validate(); err != nil {
		return state.Resource{}, fmt.Errorf("firewall target lacks durable identity: %w", err)
	}
	targetState := map[string]any{"container_id": targetResource.ContainerID(), "bindings": bindings, "attachment_ips": attachmentIPs}
	if targetResource.Driver == "firecracker" {
		providerState, err := targetResource.ProviderState()
		if err != nil {
			return state.Resource{}, fmt.Errorf("firewall %s: read target provider state: %w", n.Address.Name, err)
		}
		targetState["provider_state"] = json.RawMessage(providerState)
	}
	targetRaw, err := json.Marshal(targetState)
	if err != nil {
		return state.Resource{}, err
	}
	owner := e.topology + "/" + n.Address.String()
	spec, err := firewallRuleset(owner, cfg)
	if err != nil {
		return state.Resource{}, fmt.Errorf("firewall %s: %w", n.Address.Name, err)
	}
	policy, err := e.providers.RequirePolicy(targetResource.Driver)
	if err != nil {
		return state.Resource{}, err
	}
	policyIdentity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || policyIdentity.Validate() != nil {
		return state.Resource{}, fmt.Errorf("firewall requires durable policy identity")
	}
	target := driver.PolicyTarget{Resource: targetAddr.String(), Identity: targetIdentity, Policy: policyIdentity, State: targetRaw}
	persistedTarget, err := driver.EncodePolicyTarget(target)
	if err != nil {
		return state.Resource{}, err
	}
	var observation driver.RulesetObservation
	if discover {
		observer, ok := policy.(driver.PolicyDiscovery)
		if !ok {
			return state.Resource{}, fmt.Errorf("%w: provider %s has no policy discovery", ErrConvergenceUnsupported, targetResource.Driver)
		}
		observation, err = observer.DiscoverRuleset(ctx, target, spec)
	} else {
		observation, err = policy.ApplyRuleset(ctx, target, spec)
	}
	if err != nil {
		return state.Resource{}, fmt.Errorf("firewall %s: %w", n.Address.Name, err)
	}
	specRaw, err := json.Marshal(spec)
	if err != nil {
		return state.Resource{}, err
	}
	inst := map[string]any{
		"attach_to": targetAddr.String(), "family": string(driver.FamilyIPv4), "owner": owner,
		"table": observation.Table, "desired_digest": observation.Digest, "observed_digest": observation.Digest,
		"policy_target_state": string(persistedTarget), "policy_spec": string(specRaw), "rules": len(spec.Rules),
	}
	if err := setDesiredHash(n, inst); err != nil {
		return state.Resource{}, err
	}
	return state.Resource{Address: n.Address, Driver: targetResource.Driver, ExternalID: observation.Table, OwnerUID: policyIdentity.OwnerUID, ResourceUID: policyIdentity.ResourceUID, Generation: policyIdentity.Generation, Attributes: state.MustAttributes(inst)}, nil
}

func firewallRuleset(owner string, cfg *config.FirewallConfig) (driver.RulesetSpec, error) {
	family := driver.AddressFamily(cfg.Family)
	if family == "" {
		family = driver.FamilyIPv4
	}
	spec := driver.RulesetSpec{Owner: owner, Family: family, DefaultInput: driver.Verdict(cfg.DefaultInput), DefaultOutput: driver.Verdict(cfg.DefaultOutput), DefaultForward: driver.Verdict(cfg.DefaultForward)}
	for _, input := range cfg.Rules {
		sourcePorts, err := parsePortRanges(input.SourcePorts)
		if err != nil {
			return spec, fmt.Errorf("rule %q source_ports: %w", input.Name, err)
		}
		destinationPorts, err := parsePortRanges(input.DestinationPorts)
		if err != nil {
			return spec, fmt.Errorf("rule %q destination_ports: %w", input.Name, err)
		}
		states := make([]driver.ConnectionState, len(input.States))
		for i := range input.States {
			states[i] = driver.ConnectionState(input.States[i])
		}
		spec.Rules = append(spec.Rules, driver.PolicyRule{ID: input.Name, Direction: driver.Direction(input.Direction), SourceCIDRs: input.SourceCIDRs, DestinationCIDRs: input.DestinationCIDRs, SourcePorts: sourcePorts, DestinationPorts: destinationPorts, Protocol: driver.Protocol(input.Protocol), InputAttachment: input.InputAttachment, OutputAttachment: input.OutputAttachment, States: states, Verdict: driver.Verdict(input.Verdict), Counter: input.Counter, Log: input.Log})
	}
	return driver.NormalizeRuleset(spec)
}

func parsePortRanges(values []string) ([]driver.PortRange, error) {
	out := make([]driver.PortRange, 0, len(values))
	for _, value := range values {
		parts := strings.SplitN(value, "-", 2)
		from, err := strconv.ParseUint(parts[0], 10, 16)
		if err != nil || from == 0 {
			return nil, fmt.Errorf("invalid port range %q", value)
		}
		to := from
		if len(parts) == 2 {
			to, err = strconv.ParseUint(parts[1], 10, 16)
			if err != nil || to == 0 || from > to {
				return nil, fmt.Errorf("invalid port range %q", value)
			}
		}
		out = append(out, driver.PortRange{From: uint16(from), To: uint16(to)})
	}
	return out, nil
}

func resolvePolicyTargetAddress(ref string) (address.Address, error) {
	trimmed := strings.TrimSuffix(ref, ".id")
	addr, err := address.Parse(trimmed)
	if err != nil {
		return address.Address{}, fmt.Errorf("invalid firewall attach_to %q: %w", ref, err)
	}
	if addr.Type != "sysbox_node" && addr.Type != "sysbox_router" {
		return address.Address{}, fmt.Errorf("firewall attach_to %q must reference sysbox_node or sysbox_router", ref)
	}
	return addr, nil
}

func policyState(current state.Resource) (driver.PolicyTarget, string, error) {
	raw := current.Str("policy_target_state")
	owner := current.Str("owner")
	if raw == "" || owner == "" {
		return driver.PolicyTarget{}, "", fmt.Errorf("firewall state missing policy identity")
	}
	target, err := driver.DecodePolicyTarget(json.RawMessage(raw))
	if err == nil && target.Resource != current.Str("attach_to") {
		err = fmt.Errorf("firewall target address mismatch")
	}
	return target, owner, err
}
