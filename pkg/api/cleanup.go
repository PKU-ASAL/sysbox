package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockernet "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	netprovider "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

type CleanupReport struct {
	RunID      string          `json:"run_id"`
	Topology   string          `json:"topology,omitempty"`
	Containers []CleanupAction `json:"containers,omitempty"`
	Networks   []CleanupAction `json:"networks,omitempty"`
	MicroVMs   []CleanupAction `json:"microvms,omitempty"`
}

type CleanupAction struct {
	Resource   string `json:"resource"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func cleanupCheckpoint(ctx context.Context, checkpointPath string) (*CleanupReport, error) {
	raw, err := os.ReadFile(checkpointPath)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}

	report := &CleanupReport{RunID: cp.RunID, Topology: cp.Topology}
	var cli *client.Client
	defer func() {
		if cli != nil {
			_ = cli.Close()
		}
	}()
	for i := len(cp.Steps) - 1; i >= 0; i-- {
		step := cp.Steps[i]
		if !cleanupCandidate(step) {
			continue
		}
		switch step.Provider {
		case "docker":
			if cli == nil {
				var err error
				cli, err = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
				if err != nil {
					return nil, fmt.Errorf("docker client: %w", err)
				}
			}
			if isDockerNetworkStep(step) {
				report.Networks = append(report.Networks, cleanupDockerNetwork(ctx, cli, step))
				continue
			}
			report.Containers = append(report.Containers, cleanupDockerContainer(ctx, cli, step))
		case "network":
			report.Networks = append(report.Networks, cleanupLocalNetwork(step))
		case "firecracker":
			report.MicroVMs = append(report.MicroVMs, cleanupFirecrackerNode(ctx, step))
		}
	}
	return report, nil
}

func cleanupCandidate(step runtime.OperationStep) bool {
	return step.Kind == "resource" &&
		step.Status == runtime.OperationDone &&
		!step.StateRecorded &&
		cleanupProviderSupported(step)
}

func cleanupProviderSupported(step runtime.OperationStep) bool {
	switch step.Provider {
	case "docker":
		return true
	case "firecracker", "network":
		return step.StateResource != nil
	default:
		return false
	}
}

func isDockerNetworkStep(step runtime.OperationStep) bool {
	return step.Labels[runtime.LabelResourceType] == "sysbox_network"
}

func cleanupDockerContainer(ctx context.Context, cli *client.Client, step runtime.OperationStep) CleanupAction {
	action := CleanupAction{Resource: step.Resource, ExternalID: step.ExternalID}
	id := step.ExternalID
	if id == "" {
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
		if err != nil {
			action.Status = "error"
			action.Error = err.Error()
			return action
		}
		id = found
		action.ExternalID = id
	}
	if id == "" {
		action.Status = "not_found"
		return action
	}
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action
	}
	action.Status = "removed"
	return action
}

func cleanupDockerNetwork(ctx context.Context, cli *client.Client, step runtime.OperationStep) CleanupAction {
	action := CleanupAction{Resource: step.Resource, ExternalID: step.ExternalID}
	id := step.ExternalID
	if id == "" {
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
		if err != nil {
			action.Status = "error"
			action.Error = err.Error()
			return action
		}
		id = found
		action.ExternalID = id
	}
	if id == "" {
		action.Status = "not_found"
		return action
	}
	if err := cli.NetworkRemove(ctx, id); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action
	}
	action.Status = "removed"
	return action
}

func cleanupLocalNetwork(step runtime.OperationStep) CleanupAction {
	action := CleanupAction{Resource: step.Resource, ExternalID: step.ExternalID}
	if step.StateResource == nil {
		action.Status = "missing_state_resource"
		return action
	}
	res := stateResourceFromLog(*step.StateResource)
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
	if brName != "" {
		if err := netprovider.DeleteBridge(netprovider.BridgeConfig{NetnsName: nsName, BridgeName: brName}); err != nil {
			action.Status = "error"
			action.Error = err.Error()
			return action
		}
	}
	if err := netprovider.DeleteNetns(nsName); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action
	}
	action.Status = "removed"
	return action
}

func cleanupFirecrackerNode(ctx context.Context, step runtime.OperationStep) CleanupAction {
	action := CleanupAction{Resource: step.Resource, ExternalID: step.ExternalID}
	if step.StateResource == nil {
		action.Status = "missing_state_resource"
		return action
	}
	res := stateResourceFromLog(*step.StateResource)
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
	_ = sub.StopNode(ctx, handle)
	if err := sub.DestroyNode(ctx, handle); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action
	}
	if err := cleanupAttachedNICs(res); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action
	}
	action.Status = "removed"
	return action
}

func cleanupAttachedNICs(res state.Resource) error {
	nics, ok := res.Instance["nics"].([]any)
	if !ok {
		return nil
	}
	var errs []string
	for _, item := range nics {
		n, _ := item.(map[string]any)
		kind := util.AsString(n["kind"])
		hostEnd := util.AsString(n["host_end"])
		nsName := util.AsString(n["netns"])
		switch kind {
		case "tap":
			if err := netprovider.DeleteTapDevice(hostEnd, nsName); err != nil {
				errs = append(errs, err.Error())
			}
		case "veth":
			if err := netprovider.DeleteVethPair(netprovider.VethHandle{HostEnd: hostEnd, NetnsName: nsName}); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func stateResourceFromLog(rec runtime.StateResourceLog) state.Resource {
	return state.Resource{
		Type:     rec.Type,
		Name:     rec.Name,
		Provider: rec.Provider,
		Instance: cloneInstance(rec.Instance),
	}
}

func isNodeLikeResource(resourceType string) bool {
	switch resourceType {
	case "sysbox_node", "sysbox_router", "sysbox_actor":
		return true
	default:
		return false
	}
}

func findDockerObjectByLabels(ctx context.Context, labels map[string]string, list func(filters.Args) ([]string, error)) (string, error) {
	if len(labels) == 0 {
		return "", nil
	}
	args := filters.NewArgs()
	for _, key := range []string{runtime.LabelManaged, runtime.LabelTopology, runtime.LabelResource} {
		if value := labels[key]; value != "" {
			args.Add("label", key+"="+value)
		}
	}
	found, err := list(args)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", nil
	}
	return found[0], nil
}
