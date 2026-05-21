package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockernet "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	netprovider "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
)

type RecoverReport struct {
	RunID     string          `json:"run_id"`
	Topology  string          `json:"topology,omitempty"`
	Recovered []RecoverAction `json:"recovered,omitempty"`
	Skipped   []RecoverAction `json:"skipped,omitempty"`
}

type RecoverAction struct {
	Resource   string `json:"resource"`
	ExternalID string `json:"external_id,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func recoverCheckpoint(ctx context.Context, checkpointPath string, mgr *state.Manager, owner string) (*RecoverReport, error) {
	raw, err := os.ReadFile(checkpointPath)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}

	st, err := mgr.LoadWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	report := &RecoverReport{RunID: cp.RunID, Topology: cp.Topology}
	var cli *client.Client
	defer func() {
		if cli != nil {
			_ = cli.Close()
		}
	}()
	for _, step := range cp.Steps {
		if !recoverCandidate(step) {
			continue
		}
		var action RecoverAction
		switch step.Provider {
		case "docker":
			if cli == nil {
				var err error
				cli, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
				if err != nil {
					return nil, fmt.Errorf("docker client: %w", err)
				}
			}
			action = recoverDockerStep(ctx, cli, st, step)
		case "network":
			action = recoverLocalNetwork(st, step)
		case "firecracker":
			action = recoverFirecrackerNode(ctx, st, step)
		default:
			continue
		}
		if actionRecovered(action) {
			report.Recovered = append(report.Recovered, action)
		} else {
			report.Skipped = append(report.Skipped, action)
		}
	}
	if len(report.Recovered) > 0 {
		st.RunID = cp.RunID
		if err := mgr.SaveWithLease(ctx, st, state.LockOptions{Owner: owner}); err != nil {
			return nil, fmt.Errorf("save recovered state: %w", err)
		}
	}
	return report, nil
}

func actionRecovered(action RecoverAction) bool {
	return action.Status == "recovered" || action.Status == "recovered_not_running"
}

func recoverCandidate(step runtime.OperationStep) bool {
	return step.Kind == "resource" &&
		step.Status == runtime.OperationDone &&
		!step.StateRecorded &&
		step.StateResource != nil &&
		recoverProviderSupported(step.Provider)
}

func recoverProviderSupported(provider string) bool {
	switch provider {
	case "docker", "firecracker", "network":
		return true
	default:
		return false
	}
}

func recoverDockerStep(ctx context.Context, cli *client.Client, st *state.State, step runtime.OperationStep) RecoverAction {
	action := RecoverAction{Resource: step.Resource, ExternalID: step.ExternalID}
	rec := step.StateResource
	if rec == nil {
		action.Status = "missing_state_resource"
		return action
	}
	if existing := st.FindResource(rec.Type, rec.Name); existing != nil {
		action.Status = "already_in_state"
		return action
	}
	exists, externalID, err := dockerObjectExists(ctx, cli, step)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action
	}
	action.ExternalID = externalID
	if !exists {
		action.Status = "not_found"
		return action
	}
	adoptStateResource(st, *rec, externalID)
	action.Status = "recovered"
	return action
}

func recoverLocalNetwork(st *state.State, step runtime.OperationStep) RecoverAction {
	action := RecoverAction{Resource: step.Resource, ExternalID: step.ExternalID}
	rec := step.StateResource
	if rec == nil {
		action.Status = "missing_state_resource"
		return action
	}
	if existing := st.FindResource(rec.Type, rec.Name); existing != nil {
		action.Status = "already_in_state"
		return action
	}
	res := stateResourceFromLog(*rec)
	nsName := res.Str("netns")
	brName := res.Str("bridge")
	action.ExternalID = nsName
	if nsName == "" {
		action.Status = "missing_netns"
		return action
	}
	if !netprovider.NetnsExists(nsName) {
		action.Status = "not_found"
		return action
	}
	if brName != "" && !netprovider.BridgeExists(nsName, brName) {
		action.Status = "bridge_not_found"
		return action
	}
	adoptStateResource(st, *rec, "")
	action.Status = "recovered"
	return action
}

func recoverFirecrackerNode(ctx context.Context, st *state.State, step runtime.OperationStep) RecoverAction {
	action := RecoverAction{Resource: step.Resource, ExternalID: step.ExternalID}
	rec := step.StateResource
	if rec == nil {
		action.Status = "missing_state_resource"
		return action
	}
	if existing := st.FindResource(rec.Type, rec.Name); existing != nil {
		action.Status = "already_in_state"
		return action
	}
	res := stateResourceFromLog(*rec)
	if !isNodeLikeResource(res.Type) {
		action.Status = "unsupported_resource"
		return action
	}
	if action.ExternalID == "" {
		action.ExternalID = res.ContainerID()
	}
	sub, err := substrate.Get(res.Provider)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action
	}
	handle, err := res.ReconstructHandle(sub)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action
	}
	if !firecrackerRecoverable(res) {
		action.Status = "not_found"
		return action
	}
	if running, err := sub.NodeStatus(ctx, handle); err == nil && !running {
		action.Status = "recovered_not_running"
		adoptStateResource(st, *rec, "")
		return action
	}
	adoptStateResource(st, *rec, "")
	action.Status = "recovered"
	return action
}

func adoptStateResource(st *state.State, rec runtime.StateResourceLog, externalID string) {
	inst := cloneInstance(rec.Instance)
	if externalID != "" {
		switch rec.Type {
		case "sysbox_network":
			inst["docker_network_id"] = externalID
		case "sysbox_node", "sysbox_router", "sysbox_actor":
			inst["container_id"] = externalID
		}
	}
	st.AddResource(state.Resource{
		Type:     rec.Type,
		Name:     rec.Name,
		Provider: rec.Provider,
		Instance: inst,
	})
}

func dockerObjectExists(ctx context.Context, cli *client.Client, step runtime.OperationStep) (bool, string, error) {
	id := step.ExternalID
	if isDockerNetworkStep(step) {
		if id != "" {
			if _, err := cli.NetworkInspect(ctx, id, dockernet.InspectOptions{}); err == nil {
				return true, id, nil
			}
		}
		found, err := findDockerObjectByLabels(ctx, step.Labels, func(args filters.Args) ([]string, error) {
			items, err := cli.NetworkList(ctx, dockernet.ListOptions{Filters: args})
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(items))
			for _, item := range items {
				out = append(out, item.ID)
			}
			return out, nil
		})
		return found != "", found, err
	}
	if id != "" {
		if _, err := cli.ContainerInspect(ctx, id); err == nil {
			return true, id, nil
		}
	}
	found, err := findDockerObjectByLabels(ctx, step.Labels, func(args filters.Args) ([]string, error) {
		items, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, item.ID)
		}
		return out, nil
	})
	return found != "", found, err
}

func firecrackerRecoverable(res state.Resource) bool {
	providerExtra := res.ProviderExtra()
	if providerExtra == "" {
		return false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(providerExtra), &raw); err != nil {
		return false
	}
	if vmDir, _ := raw["vm_dir"].(string); vmDir != "" {
		if _, err := os.Stat(vmDir); err == nil {
			return true
		}
	}
	if socket, _ := raw["socket"].(string); socket != "" {
		if _, err := os.Stat(socket); err == nil {
			return true
		}
	}
	if configPath, _ := raw["config_path"].(string); configPath != "" {
		if _, err := os.Stat(configPath); err == nil {
			return true
		}
	}
	return false
}

func cloneInstance(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
