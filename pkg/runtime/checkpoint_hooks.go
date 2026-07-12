package runtime

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

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	netprovider "github.com/oslab/sysbox/pkg/provider/network"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/oslab/sysbox/pkg/util"
)

type CheckpointRecoverer interface {
	RecoverCheckpointResource(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, error)
}

type CheckpointCleaner interface {
	CleanupCheckpointResource(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error)
}

type CheckpointRecoverResult struct {
	Resource   string
	ExternalID string
	Status     string
	Error      string
}

type CheckpointCleanupClass string

const (
	CheckpointCleanupContainer CheckpointCleanupClass = "container"
	CheckpointCleanupNetwork   CheckpointCleanupClass = "network"
	CheckpointCleanupMicroVM   CheckpointCleanupClass = "microvm"
)

type CheckpointCleanupResult struct {
	Resource   string
	ExternalID string
	Status     string
	Error      string
	Class      CheckpointCleanupClass
}

func RecoverCheckpointResource(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, bool) {
	p, ok := GetResourceHandler(checkpointResourceType(step))
	if !ok {
		return CheckpointRecoverResult{}, false
	}
	hook, ok := p.(CheckpointRecoverer)
	if !ok {
		return CheckpointRecoverResult{}, false
	}
	result, err := hook.RecoverCheckpointResource(ctx, st, step)
	if err != nil {
		result = CheckpointRecoverResult{Resource: step.Resource, ExternalID: step.ExternalID, Status: "error", Error: err.Error()}
	}
	return result, true
}

func CleanupCheckpointResource(ctx context.Context, step OperationStep) (CheckpointCleanupResult, bool) {
	p, ok := GetResourceHandler(checkpointResourceType(step))
	if !ok {
		return CheckpointCleanupResult{}, false
	}
	hook, ok := p.(CheckpointCleaner)
	if !ok {
		return CheckpointCleanupResult{}, false
	}
	result, err := hook.CleanupCheckpointResource(ctx, step)
	if err != nil {
		result = CheckpointCleanupResult{Resource: step.Resource, ExternalID: step.ExternalID, Status: "error", Error: err.Error()}
	}
	return result, true
}

func SupportsCheckpointRecover(step OperationStep) bool {
	p, ok := GetResourceHandler(checkpointResourceType(step))
	if !ok {
		return false
	}
	_, ok = p.(CheckpointRecoverer)
	return ok
}

func SupportsCheckpointCleanup(step OperationStep) bool {
	p, ok := GetResourceHandler(checkpointResourceType(step))
	if !ok {
		return false
	}
	_, ok = p.(CheckpointCleaner)
	return ok
}

func checkpointResourceType(step OperationStep) string {
	if step.StateResource != nil {
		return step.StateResource.Type
	}
	return step.Labels[LabelResourceType]
}

func StateResourceFromLog(rec StateResourceLog) state.Resource {
	attributes := cloneInstance(rec.Instance)
	delete(attributes, "provider_extra")
	resource := state.Resource{
		Address:    address.Resource(rec.Type, rec.Name),
		Driver:     rec.Provider,
		Attributes: state.MustAttributes(attributes),
	}
	return resource
}

func AdoptStateResource(st *state.State, rec StateResourceLog, externalID string) {
	res := StateResourceFromLog(rec)
	if externalID != "" {
		switch rec.Type {
		case "sysbox_network":
			_ = res.SetAttribute("docker_network_id", externalID)
		case "sysbox_node", "sysbox_router", "sysbox_actor":
			_ = res.SetAttribute("container_id", externalID)
		}
	}
	st.AddResource(res)
}

func recoverStateResourceFromCheckpoint(st *state.State, step OperationStep) CheckpointRecoverResult {
	action := CheckpointRecoverResult{Resource: step.Resource, ExternalID: step.ExternalID}
	rec := step.StateResource
	if rec == nil {
		action.Status = "missing_state_resource"
		return action
	}
	if existing := st.FindResource(address.Resource(rec.Type, rec.Name)); existing != nil {
		action.Status = "already_in_state"
		return action
	}
	AdoptStateResource(st, *rec, action.ExternalID)
	action.Status = "recovered"
	return action
}

func (NetworkResourceHandler) RecoverCheckpointResource(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, error) {
	action := CheckpointRecoverResult{Resource: step.Resource, ExternalID: step.ExternalID}
	rec := step.StateResource
	if rec == nil {
		action.Status = "missing_state_resource"
		return action, nil
	}
	if existing := st.FindResource(address.Resource(rec.Type, rec.Name)); existing != nil {
		action.Status = "already_in_state"
		return action, nil
	}
	res := StateResourceFromLog(*rec)
	if res.Driver == "docker" || res.IsNAT() {
		return recoverDockerManagedNetwork(ctx, st, step)
	}
	nsName := res.Str("netns")
	brName := res.Str("bridge")
	action.ExternalID = nsName
	if nsName == "" {
		action.Status = "missing_netns"
		return action, nil
	}
	if !netprovider.NetnsExists(nsName) {
		action.Status = "not_found"
		return action, nil
	}
	if brName != "" && !netprovider.BridgeExists(nsName, brName) {
		action.Status = "bridge_not_found"
		return action, nil
	}
	AdoptStateResource(st, *rec, "")
	action.Status = "recovered"
	return action, nil
}

func (NetworkResourceHandler) CleanupCheckpointResource(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error) {
	action := CheckpointCleanupResult{Resource: step.Resource, ExternalID: step.ExternalID, Class: CheckpointCleanupNetwork}
	if step.StateResource == nil {
		if step.Provider == "docker" {
			return cleanupDockerManagedNetwork(ctx, step)
		}
		action.Status = "missing_state_resource"
		return action, nil
	}
	res := StateResourceFromLog(*step.StateResource)
	if res.Driver == "docker" || res.IsNAT() {
		return cleanupDockerManagedNetwork(ctx, step)
	}
	nsName := res.Str("netns")
	brName := res.Str("bridge")
	action.ExternalID = nsName
	if nsName == "" {
		action.Status = "missing_netns"
		return action, nil
	}
	if !netprovider.NetnsExists(nsName) {
		action.Status = "not_found"
		return action, nil
	}
	if brName != "" {
		if err := netprovider.DeleteBridge(netprovider.BridgeConfig{NetnsName: nsName, BridgeName: brName}); err != nil {
			action.Status = "error"
			action.Error = err.Error()
			return action, nil
		}
	}
	if err := netprovider.DeleteNetns(nsName); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.Status = "removed"
	return action, nil
}

func (NodeResourceHandler) RecoverCheckpointResource(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, error) {
	return recoverNodeLikeCheckpoint(ctx, st, step)
}

func (RouterResourceHandler) RecoverCheckpointResource(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, error) {
	return recoverNodeLikeCheckpoint(ctx, st, step)
}

func (ActorResourceHandler) RecoverCheckpointResource(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, error) {
	if step.StateResource != nil {
		res := StateResourceFromLog(*step.StateResource)
		if res.Str("position") == "internal" {
			return recoverStateResourceFromCheckpoint(st, step), nil
		}
	}
	return recoverNodeLikeCheckpoint(ctx, st, step)
}

func (NodeResourceHandler) CleanupCheckpointResource(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error) {
	return cleanupNodeLikeCheckpoint(ctx, step)
}

func (RouterResourceHandler) CleanupCheckpointResource(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error) {
	return cleanupNodeLikeCheckpoint(ctx, step)
}

func (ActorResourceHandler) CleanupCheckpointResource(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error) {
	if step.StateResource != nil {
		res := StateResourceFromLog(*step.StateResource)
		if res.Str("position") == "internal" {
			return CheckpointCleanupResult{Resource: step.Resource, ExternalID: step.ExternalID, Status: "no_owned_external_resource", Class: CheckpointCleanupContainer}, nil
		}
	}
	return cleanupNodeLikeCheckpoint(ctx, step)
}

func recoverNodeLikeCheckpoint(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, error) {
	action := CheckpointRecoverResult{Resource: step.Resource, ExternalID: step.ExternalID}
	rec := step.StateResource
	if rec == nil {
		return recoverDockerNodeLikeByLabels(ctx, st, step)
	}
	if existing := st.FindResource(address.Resource(rec.Type, rec.Name)); existing != nil {
		action.Status = "already_in_state"
		return action, nil
	}
	res := StateResourceFromLog(*rec)
	if action.ExternalID == "" {
		action.ExternalID = res.ContainerID()
	}
	if res.Driver == "docker" {
		return recoverDockerNodeLike(ctx, st, step)
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(res.Driver)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(res.Driver)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	handle, err := res.ReconstructHandle(stateDriver)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	obs, err := nodeDriver.ObserveNode(ctx, handle)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	recovery := DecideNodeRecovery(RecoveryInput{
		Context:              RecoveryContextCheckpoint,
		ResourceType:         rec.Type,
		Provider:             rec.Provider,
		HasState:             false,
		HasCheckpoint:        true,
		StateRecorded:        step.StateRecorded,
		RecoverableArtifacts: FirecrackerRecoverableArtifacts(res),
		Observation:          obs,
	})
	switch recovery.Decision {
	case controlplane.RecoveryDecisionAdopt:
		if adopted, err := nodeDriver.AdoptNode(ctx, handle); err == nil {
			if inst := substrate.HandlePublicAttributes(adopted); len(inst) > 0 {
				for k, v := range inst {
					rec.Instance[k] = v
				}
			}
			AdoptStateResource(st, *rec, "")
			if blob, err := stateDriver.MarshalProviderState(adopted); err == nil && len(blob) > 0 {
				if resource := st.FindResource(address.Resource(rec.Type, rec.Name)); resource != nil {
					_ = resource.SetProviderState(blob)
				}
			}
			action.Status = "recovered_adopted"
			return action, nil
		}
		AdoptStateResource(st, *rec, "")
		action.Status = "recovered"
	case controlplane.RecoveryDecisionRecoverState:
		AdoptStateResource(st, *rec, "")
		action.Status = "recovered_not_running"
	case controlplane.RecoveryDecisionNoop:
		action.Status = "already_in_state"
	case controlplane.RecoveryDecisionNotFound:
		action.Status = "not_found"
		action.Error = recovery.Reason
	default:
		action.Status = "error"
		action.Error = recovery.Reason
	}
	return action, nil
}

func cleanupNodeLikeCheckpoint(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error) {
	action := CheckpointCleanupResult{Resource: step.Resource, ExternalID: step.ExternalID, Class: CheckpointCleanupContainer}
	rec := step.StateResource
	if rec == nil {
		return cleanupDockerNodeLikeByLabels(ctx, step)
	}
	res := StateResourceFromLog(*rec)
	if res.Driver == "firecracker" {
		action.Class = CheckpointCleanupMicroVM
	}
	if action.ExternalID == "" {
		action.ExternalID = res.ContainerID()
	}
	if res.Driver == "docker" {
		return cleanupDockerNodeLike(ctx, step)
	}
	nodeDriver, err := driver.DefaultRegistry.RequireNode(res.Driver)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	stateDriver, err := driver.DefaultRegistry.RequireNodeState(res.Driver)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	handle, err := res.ReconstructHandle(stateDriver)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	_ = nodeDriver.StopNode(ctx, handle)
	if err := nodeDriver.DestroyNode(ctx, handle); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	if err := cleanupAttachedNICs(res); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.Status = "removed"
	return action, nil
}

func recoverDockerManagedNetwork(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, error) {
	action := CheckpointRecoverResult{Resource: step.Resource, ExternalID: step.ExternalID}
	rec := step.StateResource
	if rec == nil {
		action.Status = "missing_state_resource"
		return action, nil
	}
	if existing := st.FindResource(address.Resource(rec.Type, rec.Name)); existing != nil {
		action.Status = "already_in_state"
		return action, nil
	}
	exists, id, err := dockerObjectExists(ctx, step, true)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.ExternalID = id
	if !exists {
		action.Status = "not_found"
		return action, nil
	}
	AdoptStateResource(st, *rec, id)
	action.Status = "recovered"
	return action, nil
}

func recoverDockerNodeLike(ctx context.Context, st *state.State, step OperationStep) (CheckpointRecoverResult, error) {
	action := CheckpointRecoverResult{Resource: step.Resource, ExternalID: step.ExternalID}
	rec := step.StateResource
	if rec == nil {
		return recoverDockerNodeLikeByLabels(ctx, st, step)
	}
	if existing := st.FindResource(address.Resource(rec.Type, rec.Name)); existing != nil {
		action.Status = "already_in_state"
		return action, nil
	}
	exists, id, err := dockerObjectExists(ctx, step, false)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.ExternalID = id
	if !exists {
		action.Status = "not_found"
		return action, nil
	}
	AdoptStateResource(st, *rec, id)
	action.Status = "recovered"
	return action, nil
}

func recoverDockerNodeLikeByLabels(ctx context.Context, _ *state.State, step OperationStep) (CheckpointRecoverResult, error) {
	action := CheckpointRecoverResult{Resource: step.Resource, ExternalID: step.ExternalID}
	exists, id, err := dockerObjectExists(ctx, step, false)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.ExternalID = id
	if !exists {
		action.Status = "not_found"
		return action, nil
	}
	action.Status = "missing_state_resource"
	return action, nil
}

func cleanupDockerManagedNetwork(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error) {
	action := CheckpointCleanupResult{Resource: step.Resource, ExternalID: step.ExternalID, Class: CheckpointCleanupNetwork}
	id, err := findDockerObjectForStep(ctx, step, true)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.ExternalID = id
	if id == "" {
		action.Status = "not_found"
		return action, nil
	}
	cli, err := dockerClient()
	if err != nil {
		return action, err
	}
	defer cli.Close()
	if err := cli.NetworkRemove(ctx, id); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.Status = "removed"
	return action, nil
}

func cleanupDockerNodeLike(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error) {
	return cleanupDockerNodeLikeByLabels(ctx, step)
}

func cleanupDockerNodeLikeByLabels(ctx context.Context, step OperationStep) (CheckpointCleanupResult, error) {
	action := CheckpointCleanupResult{Resource: step.Resource, ExternalID: step.ExternalID, Class: CheckpointCleanupContainer}
	id, err := findDockerObjectForStep(ctx, step, false)
	if err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.ExternalID = id
	if id == "" {
		action.Status = "not_found"
		return action, nil
	}
	cli, err := dockerClient()
	if err != nil {
		return action, err
	}
	defer cli.Close()
	if err := cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		action.Status = "error"
		action.Error = err.Error()
		return action, nil
	}
	action.Status = "removed"
	return action, nil
}

func dockerObjectExists(ctx context.Context, step OperationStep, network bool) (bool, string, error) {
	id, err := findDockerObjectForStep(ctx, step, network)
	return id != "", id, err
}

func findDockerObjectForStep(ctx context.Context, step OperationStep, network bool) (string, error) {
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	id := step.ExternalID
	if network {
		if id != "" {
			if _, err := cli.NetworkInspect(ctx, id, dockernet.InspectOptions{}); err == nil {
				return id, nil
			}
		}
		return findDockerObjectByLabels(ctx, step.Labels, func(args filters.Args) ([]string, error) {
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
	}
	if id != "" {
		if _, err := cli.ContainerInspect(ctx, id); err == nil {
			return id, nil
		}
	}
	return findDockerObjectByLabels(ctx, step.Labels, func(args filters.Args) ([]string, error) {
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
}

func dockerClient() (*client.Client, error) {
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

func findDockerObjectByLabels(_ context.Context, labels map[string]string, list func(filters.Args) ([]string, error)) (string, error) {
	if len(labels) == 0 {
		return "", nil
	}
	args := filters.NewArgs()
	for _, key := range []string{LabelManaged, LabelTopology, LabelResource} {
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

func cleanupAttachedNICs(res state.Resource) error {
	nics, ok := res.AttributeMap()["nics"].([]any)
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

func FirecrackerRecoverableArtifacts(res state.Resource) bool {
	providerExtra, err := res.ProviderState()
	if err != nil || len(providerExtra) == 0 {
		return false
	}
	var raw map[string]any
	if err := json.Unmarshal(providerExtra, &raw); err != nil {
		return false
	}
	for _, key := range []string{"vm_dir", "socket", "config_path"} {
		if path, _ := raw[key].(string); path != "" {
			if _, err := os.Stat(path); err == nil {
				return true
			}
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
