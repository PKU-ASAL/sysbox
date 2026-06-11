package runtime

import (
	"context"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
)

func EvaluateTopologyHealth(ctx context.Context, st *state.State) controlplane.TopologyHealth {
	out := controlplane.TopologyHealth{
		Status:    controlplane.ResourceHealthHealthy,
		Resources: make([]controlplane.ResourceHealth, 0, len(st.Resources)),
	}
	for _, res := range st.Resources {
		rh := EvaluateResourceHealth(ctx, &res)
		out.Resources = append(out.Resources, rh)
		switch rh.Status {
		case controlplane.ResourceHealthHealthy:
			out.Healthy++
		case controlplane.ResourceHealthDrifted:
			out.Drifted++
		default:
			out.Unknown++
		}
	}
	if out.Drifted > 0 {
		out.Status = controlplane.ResourceHealthDrifted
	} else if out.Unknown > 0 {
		out.Status = controlplane.ResourceHealthUnknown
	}
	return out
}

func EvaluateResourceHealth(ctx context.Context, res *state.Resource) controlplane.ResourceHealth {
	rh := controlplane.ResourceHealth{
		Resource: res.Type + "." + res.Name,
		Type:     res.Type,
		Name:     res.Name,
		Provider: res.Provider,
		Status:   controlplane.ResourceHealthHealthy,
	}
	if provider, ok := GetResourceProvider(res.Type); ok {
		result, err := provider.Read(ctx, *res)
		rh.Reason = result.Reason
		rh.Decision = result.Decision
		rh.Observation = result.Observation
		rh.Checks = result.Checks
		if err != nil {
			status, reason, known := classifyResourceReadError(err)
			if !known || status == ResourceReadUnknown {
				rh.Status = controlplane.ResourceHealthUnknown
				rh.Decision = controlplane.RecoveryDecisionUnknown
				rh.Reason = reason
				return rh
			}
			rh.Status = controlplane.ResourceHealthDrifted
			rh.Decision = controlplane.RecoveryDecisionMarkDrift
			rh.Reason = reason
			return rh
		}
		return rh
	}
	rh.Reason = "resource has no runtime health probe"
	return rh
}

func networkAttachmentsCheck(res *state.Resource) (bool, string) {
	items, ok := res.Instance["nics"].([]any)
	if !ok {
		return true, ""
	}
	for _, item := range items {
		nic, _ := item.(map[string]any)
		kind, _ := nic["kind"].(string)
		switch kind {
		case "veth", "tap":
			nsName, _ := nic["netns"].(string)
			hostEnd, _ := nic["host_end"].(string)
			if !network.LinkExists(nsName, hostEnd) {
				return false, "network attachment missing"
			}
		case "docker-nat":
			continue
		}
	}
	return true, ""
}
