package hostexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/stretchr/testify/require"
)

type agentObservedResource struct {
	kind             string
	exists, unknown  bool
	creates, deletes int
}

func (h *agentObservedResource) Type() string { return h.kind }
func (h *agentObservedResource) Schema() runtime.ResourceSchema {
	return runtime.ResourceSchema{Type: h.kind, Version: 1}
}
func (h *agentObservedResource) Read(_ context.Context, r state.Resource, providers *driver.Registry) (runtime.ResourceReadResult, error) {
	s := state.ResourceAbsent
	if h.exists {
		s = state.ResourcePresent
	}
	if h.unknown {
		s = state.ResourceUnknown
	}
	return runtime.ResourceReadResult{Status: s, Resource: r}, nil
}
func (h *agentObservedResource) Discover(ctx context.Context, _ *runtime.ProviderContext, n *graph.Node) (runtime.ResourceReadResult, error) {
	return h.Read(ctx, state.Resource{Address: n.Address}, driver.DefaultRegistry)
}
func (h *agentObservedResource) PlanDiff(n *graph.Node, r *state.Resource) (model.PlannedChange, error) {
	action := model.PlanActionNoop
	if r == nil {
		action = model.PlanActionCreate
	}
	return model.PlannedChange{Address: n.Address, Action: action}, nil
}
func (h *agentObservedResource) Create(_ context.Context, _ *runtime.ProviderContext, n *graph.Node) (state.Resource, error) {
	h.exists = true
	h.creates++
	return state.Resource{Address: n.Address, ExternalID: "owned-instance"}, nil
}
func (h *agentObservedResource) Delete(context.Context, *runtime.ProviderContext, state.Resource) error {
	h.exists = false
	h.deletes++
	return nil
}
func (h *agentObservedResource) ExternalID(r state.Resource) string { return r.ExternalID }
func (h *agentObservedResource) DecodeResource(config.ResourceBlock, string, *hcl.EvalContext, *driver.Registry) (any, []address.Address, error) {
	return nil, nil, nil
}

type responseLostRepository struct {
	runtime.ActionRepository
	lose      bool
	failPatch bool
}

func (r *responseLostRepository) CommitActionOutcome(ctx context.Context, a runtime.ActionRecord, p *runtime.StatePatch) error {
	if p != nil && r.failPatch {
		return errors.New("commit unavailable")
	}
	if err := r.ActionRepository.CommitActionOutcome(ctx, a, p); err != nil {
		return err
	}
	if p != nil && r.lose {
		r.lose = false
		return errors.New("commit response lost")
	}
	return nil
}

var observedResourceCounter atomic.Uint64

type observedAgentBridge struct {
	*LocalBridge
	repository runtime.ActionRepository
}

func (b *observedAgentBridge) ActionRepository(string) (runtime.ActionRepository, error) {
	return b.repository, nil
}
func (b *observedAgentBridge) ParentRun(_ context.Context, id string) (*controlplane.Run, error) {
	b.Mu.Lock()
	defer b.Mu.Unlock()
	if run := b.Runs[id]; run != nil {
		return run, nil
	}
	return nil, errors.New("parent not found")
}

func TestAgentObservedLifecycleSurvivesLostCommitResponse(t *testing.T) {
	h := &agentObservedResource{kind: fmt.Sprintf("agent_observed_resource_%d", observedResourceCounter.Add(1))}
	runtime.RegisterResourceHandler(h)
	dir := t.TempDir()
	hclPath := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(hclPath, []byte(fmt.Sprintf(`resource %q "node" {}`, h.kind)), 0600))
	local := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: hclPath, StatePath: filepath.Join(dir, "state.json"), RunsDir: dir, Log: io.Discard})
	repository, err := local.ActionRepository("lab")
	require.NoError(t, err)
	fault := &responseLostRepository{ActionRepository: repository, lose: true}
	bridge := &observedAgentBridge{LocalBridge: local, repository: fault}
	first := &controlplane.Run{ID: "first", Topology: "lab", Op: "apply"}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), first)
	require.Equal(t, controlplane.RunFailed, first.Status)
	require.Contains(t, first.Err, "commit response lost")
	require.Equal(t, 1, h.creates)
	retry := &controlplane.Run{ID: "retry", Topology: "lab", Op: "apply", ParentID: "first"}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), retry)
	require.Equal(t, controlplane.RunDone, retry.Status, retry.Err)
	require.Equal(t, 1, h.creates)
	h.unknown = true
	blocked := &controlplane.Run{ID: "blocked", Topology: "lab", Op: "destroy"}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), blocked)
	require.Equal(t, controlplane.RunFailed, blocked.Status)
	require.Zero(t, h.deletes)
	h.unknown = false
	destroy := &controlplane.Run{ID: "destroy", Topology: "lab", Op: "destroy"}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), destroy)
	require.Equal(t, controlplane.RunDone, destroy.Status, destroy.Err)
	require.Equal(t, 1, h.deletes)
	stored, err := repository.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	require.Empty(t, stored.Resources)
	actions, err := repository.LoadActions(context.Background(), "lab")
	require.NoError(t, err)
	require.Len(t, actions, 2)
	for _, a := range actions {
		require.Equal(t, runtime.ActionSucceeded, a.Status)
	}
}

func TestAgentDestroyDoesNotIgnoreUnresolvedCreate(t *testing.T) {
	h := &agentObservedResource{kind: fmt.Sprintf("agent_observed_resource_%d", observedResourceCounter.Add(1))}
	runtime.RegisterResourceHandler(h)
	dir := t.TempDir()
	hclPath := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(hclPath, []byte(fmt.Sprintf(`resource %q "node" {}`, h.kind)), 0600))
	local := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: hclPath, StatePath: filepath.Join(dir, "state.json"), RunsDir: dir, Log: io.Discard})
	repo, err := local.ActionRepository("lab")
	require.NoError(t, err)
	bridge := &observedAgentBridge{LocalBridge: local, repository: &responseLostRepository{ActionRepository: repo, failPatch: true}}
	first := &controlplane.Run{ID: "first", Topology: "lab", Op: "apply"}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), first)
	require.Equal(t, controlplane.RunFailed, first.Status)
	stored, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	require.Empty(t, stored.Resources)
	destroy := &controlplane.Run{ID: "destroy", Topology: "lab", Op: "destroy"}
	NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), destroy)
	require.Equal(t, controlplane.RunFailed, destroy.Status)
	require.Contains(t, destroy.Err, "pending action")
	require.Zero(t, h.deletes)
}
