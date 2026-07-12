package runtime

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
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
		Resource: res.Address.String(),
		Type:     res.Address.Type,
		Name:     res.Address.Name,
		Provider: res.Driver,
		Status:   controlplane.ResourceHealthHealthy,
	}
	if provider, ok := GetResourceHandler(res.Address.Type); ok {
		result, err := provider.Read(ctx, *res)
		rh.Reason = result.Reason
		rh.Decision = result.Decision
		rh.Observation = result.Observation
		rh.Checks = result.Checks
		if err != nil {
			rh.Status = controlplane.ResourceHealthUnknown
			rh.Decision = controlplane.RecoveryDecisionUnknown
			rh.Reason = err.Error()
			return rh
		}
		switch result.Status {
		case state.ResourceAbsent, state.ResourceDrifted:
			rh.Status = controlplane.ResourceHealthDrifted
			rh.Decision = controlplane.RecoveryDecisionMarkDrift
		case state.ResourceUnknown:
			rh.Status = controlplane.ResourceHealthUnknown
			rh.Decision = controlplane.RecoveryDecisionUnknown
		}
		return rh
	}
	rh.Reason = "resource has no runtime health probe"
	return rh
}
