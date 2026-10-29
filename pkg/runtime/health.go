package runtime

import (
	"context"

	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

func EvaluateTopologyHealth(ctx context.Context, st *state.State, providers *driver.Registry) model.TopologyHealth {
	out := model.TopologyHealth{
		Status:    model.ResourceHealthHealthy,
		Resources: make([]model.ResourceHealth, 0, len(st.Resources)),
	}
	for _, res := range st.Resources {
		rh := evaluateResourceHealth(ctx, &res, st, providers)
		out.Resources = append(out.Resources, rh)
		switch rh.Status {
		case model.ResourceHealthHealthy:
			out.Healthy++
		case model.ResourceHealthDrifted:
			out.Drifted++
		default:
			out.Unknown++
		}
	}
	if out.Drifted > 0 {
		out.Status = model.ResourceHealthDrifted
	} else if out.Unknown > 0 {
		out.Status = model.ResourceHealthUnknown
	}
	return out
}

func EvaluateResourceHealth(ctx context.Context, res *state.Resource, providers *driver.Registry) model.ResourceHealth {
	return evaluateResourceHealth(ctx, res, nil, providers)
}

func evaluateResourceHealth(ctx context.Context, res *state.Resource, st *state.State, providers *driver.Registry) model.ResourceHealth {
	rh := model.ResourceHealth{
		Resource: res.Address.String(),
		Type:     res.Address.Type,
		Name:     res.Address.Name,
		Provider: res.Driver,
		Status:   model.ResourceHealthHealthy,
	}
	if provider, ok := GetResourceHandler(res.Address.Type); ok {
		result, err := readResourceWithAttachments(ctx, provider, *res, st, providers)
		rh.Reason = result.Reason
		rh.Decision = result.Decision
		rh.Observation = result.Observation
		rh.Checks = result.Checks
		if err != nil {
			rh.Status = model.ResourceHealthUnknown
			rh.Decision = model.RecoveryDecisionUnknown
			rh.Reason = err.Error()
			return rh
		}
		switch result.Status {
		case state.ResourceAbsent, state.ResourceDrifted:
			rh.Status = model.ResourceHealthDrifted
			rh.Decision = model.RecoveryDecisionMarkDrift
		case state.ResourceUnknown:
			rh.Status = model.ResourceHealthUnknown
			rh.Decision = model.RecoveryDecisionUnknown
		}
		return rh
	}
	rh.Reason = "resource has no runtime health probe"
	return rh
}
