package runtime

import (
	"context"
	"os"

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
		if _, err := provider.Read(ctx, *res); err != nil {
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
	}
	switch res.Type {
	case "sysbox_node", "sysbox_router", "sysbox_actor":
		return evaluateNodeHealth(ctx, res, rh)
	case "sysbox_network":
		return evaluateNetworkHealth(res, rh)
	case "sysbox_kernel":
		return evaluateKernelHealth(res, rh)
	default:
		rh.Reason = "resource has no runtime health probe"
		return rh
	}
}

func evaluateNodeHealth(ctx context.Context, res *state.Resource, rh ResourceHealth) ResourceHealth {
	if res.ContainerID() == "" {
		rh.Status = ResourceHealthDrifted
		rh.Decision = RecoveryDecisionMarkDrift
		rh.Reason = "node has no persisted external id"
		return rh
	}
	sub, err := substrate.Get(res.Provider)
	if err != nil {
		rh.Status = ResourceHealthUnknown
		rh.Decision = RecoveryDecisionUnknown
		rh.Reason = err.Error()
		return rh
	}
	handle, err := res.ReconstructHandle(sub)
	if err != nil {
		rh.Status = ResourceHealthUnknown
		rh.Decision = RecoveryDecisionUnknown
		rh.Reason = err.Error()
		return rh
	}
	obs, err := sub.ObserveNode(ctx, handle)
	if err != nil {
		rh.Status = ResourceHealthUnknown
		rh.Decision = RecoveryDecisionUnknown
		rh.Reason = err.Error()
		return rh
	}
	rh.Observation = &obs
	recovery := DecideNodeRecovery(RecoveryInput{
		Context:      RecoveryContextRefresh,
		ResourceType: res.Type,
		Provider:     res.Provider,
		HasState:     true,
		Observation:  obs,
	})
	rh.Decision = recovery.Decision
	rh.Reason = recovery.Reason
	switch recovery.Decision {
	case RecoveryDecisionNoop:
		rh.Status = ResourceHealthHealthy
	case RecoveryDecisionUnknown:
		rh.Status = ResourceHealthUnknown
	default:
		rh.Status = ResourceHealthDrifted
	}
	if rh.Status != ResourceHealthHealthy {
		return rh
	}
	checks := map[string]ResourceCheckHealth{}
	if ok, reason := networkAttachmentsCheck(res); !ok {
		checks["network_attachments"] = ResourceCheckHealth{OK: false, Reason: reason}
		rh.Status = ResourceHealthDrifted
		rh.Decision = RecoveryDecisionMarkDrift
		rh.Reason = reason
	}
	if len(checks) > 0 {
		rh.Checks = checks
	}
	return rh
}

func evaluateNetworkHealth(res *state.Resource, rh ResourceHealth) ResourceHealth {
	nsName := res.NetNS()
	brName := res.Bridge()
	if nsName == "" {
		rh.Reason = "network has no isolated namespace"
		return rh
	}
	checks := map[string]ResourceCheckHealth{
		"netns": {OK: network.NetnsExists(nsName)},
	}
	if !checks["netns"].OK {
		checks["netns"] = ResourceCheckHealth{OK: false, Reason: "network namespace missing"}
		rh.Status = ResourceHealthDrifted
		rh.Decision = RecoveryDecisionMarkDrift
		rh.Reason = "network namespace missing"
	}
	if brName != "" {
		ok := network.BridgeExists(nsName, brName)
		checks["bridge"] = ResourceCheckHealth{OK: ok}
		if !ok {
			checks["bridge"] = ResourceCheckHealth{OK: false, Reason: "bridge missing"}
			rh.Status = ResourceHealthDrifted
			rh.Decision = RecoveryDecisionMarkDrift
			rh.Reason = "bridge missing"
		}
	}
	rh.Checks = checks
	return rh
}

func evaluateKernelHealth(res *state.Resource, rh ResourceHealth) ResourceHealth {
	path := res.Str("path")
	if path == "" {
		rh.Status = ResourceHealthDrifted
		rh.Decision = RecoveryDecisionMarkDrift
		rh.Reason = "kernel path missing from state"
		return rh
	}
	if _, err := os.Stat(path); err != nil {
		rh.Status = ResourceHealthDrifted
		rh.Decision = RecoveryDecisionMarkDrift
		rh.Reason = err.Error()
		return rh
	}
	rh.Checks = map[string]ResourceCheckHealth{
		"file": {OK: true},
	}
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
