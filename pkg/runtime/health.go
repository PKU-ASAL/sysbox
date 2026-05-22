package runtime

import (
	"context"

	"github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type ResourceHealthStatus string

const (
	ResourceHealthHealthy ResourceHealthStatus = "healthy"
	ResourceHealthDrifted ResourceHealthStatus = "drifted"
	ResourceHealthUnknown ResourceHealthStatus = "unknown"
)

type TopologyHealth struct {
	Status    ResourceHealthStatus `json:"status"`
	Healthy   int                  `json:"healthy"`
	Drifted   int                  `json:"drifted"`
	Unknown   int                  `json:"unknown"`
	Resources []ResourceHealth     `json:"resources"`
}

type ResourceHealth struct {
	Resource    string                         `json:"resource"`
	Type        string                         `json:"type"`
	Name        string                         `json:"name"`
	Provider    string                         `json:"provider,omitempty"`
	Status      ResourceHealthStatus           `json:"status"`
	Reason      string                         `json:"reason,omitempty"`
	Decision    RecoveryDecision               `json:"decision,omitempty"`
	Observation *substrate.NodeObservation     `json:"observation,omitempty"`
	Checks      map[string]ResourceCheckHealth `json:"checks,omitempty"`
}

type ResourceCheckHealth struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

func EvaluateTopologyHealth(ctx context.Context, st *state.State) TopologyHealth {
	out := TopologyHealth{
		Status:    ResourceHealthHealthy,
		Resources: make([]ResourceHealth, 0, len(st.Resources)),
	}
	for _, res := range st.Resources {
		rh := EvaluateResourceHealth(ctx, &res)
		out.Resources = append(out.Resources, rh)
		switch rh.Status {
		case ResourceHealthHealthy:
			out.Healthy++
		case ResourceHealthDrifted:
			out.Drifted++
		default:
			out.Unknown++
		}
	}
	if out.Drifted > 0 {
		out.Status = ResourceHealthDrifted
	} else if out.Unknown > 0 {
		out.Status = ResourceHealthUnknown
	}
	return out
}

func EvaluateResourceHealth(ctx context.Context, res *state.Resource) ResourceHealth {
	rh := ResourceHealth{
		Resource: res.Type + "." + res.Name,
		Type:     res.Type,
		Name:     res.Name,
		Provider: res.Provider,
		Status:   ResourceHealthHealthy,
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
				rh.Status = ResourceHealthUnknown
				rh.Decision = RecoveryDecisionUnknown
				rh.Reason = reason
				return rh
			}
			rh.Status = ResourceHealthDrifted
			rh.Decision = RecoveryDecisionMarkDrift
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
