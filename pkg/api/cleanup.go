package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/oslab/sysbox/pkg/runtime"
)

type CleanupReport struct {
	RunID      string          `json:"run_id"`
	Topology   string          `json:"topology,omitempty"`
	Containers []CleanupAction `json:"containers,omitempty"`
	Networks   []CleanupAction `json:"networks,omitempty"`
}

type CleanupAction struct {
	Resource   string `json:"resource"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

func cleanupCheckpointDocker(ctx context.Context, checkpointPath string) (*CleanupReport, error) {
	raw, err := os.ReadFile(checkpointPath)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	var cp runtime.OperationCheckpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, fmt.Errorf("decode checkpoint: %w", err)
	}

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	report := &CleanupReport{RunID: cp.RunID, Topology: cp.Topology}
	for i := len(cp.Steps) - 1; i >= 0; i-- {
		step := cp.Steps[i]
		if !cleanupCandidate(step) {
			continue
		}
		switch step.Resource {
		case "state":
			continue
		}
		if isDockerNetworkStep(step) {
			report.Networks = append(report.Networks, cleanupDockerNetwork(ctx, cli, step))
			continue
		}
		report.Containers = append(report.Containers, cleanupDockerContainer(ctx, cli, step))
	}
	return report, nil
}

func cleanupCandidate(step runtime.OperationStep) bool {
	return step.Kind == "resource" &&
		step.Status == runtime.OperationDone &&
		step.Provider == "docker" &&
		!step.StateRecorded
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
			items, err := cli.NetworkList(ctx, network.ListOptions{Filters: args})
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
