package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type continuationDriver struct {
	substrate.BaseSubstrate
	driver.NIC               // no attachments in this fixture; an unexpected NIC call fails
	deleted                  map[string]bool
	created                  map[string]bool
	destroyCalls, applyCalls int
}

func (*continuationDriver) Capabilities() substrate.Capabilities { return substrate.Capabilities{} }
func (*continuationDriver) CreateNode(context.Context, substrate.NodeSpec) (substrate.NodeHandle, error) {
	return substrate.NodeHandle{}, errors.New("unexpected create")
}
func (*continuationDriver) StartNode(context.Context, substrate.NodeHandle) error { return nil }
func (*continuationDriver) StopNode(context.Context, substrate.NodeHandle) error {
	return errors.New("unexpected stop")
}
func (*continuationDriver) DestroyNode(context.Context, substrate.NodeHandle) error {
	return errors.New("unexpected destroy")
}
func (*continuationDriver) NodeStatus(context.Context, substrate.NodeHandle) (bool, error) {
	return true, nil
}
func (d *continuationDriver) ObserveNode(_ context.Context, h substrate.NodeHandle) (substrate.NodeObservation, error) {
	if d.deleted[h.ID] {
		return substrate.NodeObservation{Status: substrate.NodeStatusMissing}, nil
	}
	return substrate.NodeObservation{Exists: true, Running: true, Healthy: true, Status: substrate.NodeStatusRunning}, nil
}
func (*continuationDriver) PrepareReset(_ context.Context, req substrate.ResetRequest) (substrate.ResetHandle, error) {
	return substrate.ResetHandle{Provider: req.Current.ID}, nil
}
func (d *continuationDriver) DestroyReset(_ context.Context, h substrate.ResetHandle) error {
	d.destroyCalls++
	d.deleted[h.Provider.(string)] = true
	return nil
}
func (d *continuationDriver) ApplyReset(ctx context.Context, h substrate.ResetHandle) (substrate.NodeHandle, error) {
	identity, ok := substrate.OperationIdentityFromContext(ctx)
	if !ok || identity.Validate() != nil || identity.Generation != 2 {
		return substrate.NodeHandle{}, errors.New("missing generation fence")
	}
	d.applyCalls++
	d.created[h.Provider.(string)] = true
	return substrate.NodeHandle{ID: "new-" + h.Provider.(string)}, nil
}
func (d *continuationDriver) ObserveReset(_ context.Context, h substrate.ResetHandle) (substrate.ResetObservation, error) {
	old := h.Provider.(string)
	if d.created[old] {
		return substrate.ResetObservation{Phase: substrate.ResetPhaseComplete, Converged: true, NewExternalID: "new-" + old}, nil
	}
	return substrate.ResetObservation{Phase: substrate.ResetPhasePrepared}, nil
}
func (*continuationDriver) CleanupReset(context.Context, substrate.ResetHandle) error { return nil }
func (*continuationDriver) MarshalResetHandle(h substrate.ResetHandle) (json.RawMessage, error) {
	return json.Marshal(h.Provider)
}
func (*continuationDriver) UnmarshalResetHandle(raw json.RawMessage) (substrate.ResetHandle, error) {
	var old string
	err := json.Unmarshal(raw, &old)
	return substrate.ResetHandle{Provider: old}, err
}

type captureResetBatch struct {
	*Repository
	batch []runtime.ActionRecord
}

func (r *captureResetBatch) CommitResetIntents(_ context.Context, batch []runtime.ActionRecord) error {
	r.batch = append([]runtime.ActionRecord(nil), batch...)
	return errors.New("capture original approval before effects")
}

func TestRuntimeContinuationWithRealRepository(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			d := &continuationDriver{deleted: map[string]bool{}, created: map[string]bool{}}
			previous := driver.DefaultRegistry
			driver.DefaultRegistry = driver.NewRegistry()
			t.Cleanup(func() { driver.DefaultRegistry = previous })
			require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "continuation-test", Version: "test", Node: d, NodeState: d, NIC: d, Reset: d}))
			ctx := context.Background()
			mgr := state.NewManagerWithBackend(r.StateBackend("lab"))
			st, err := mgr.Load()
			require.NoError(t, err)
			g := graph.New()
			image := address.Resource("sysbox_image", "base")
			require.NoError(t, g.AddNode(image, nil))
			require.NoError(t, g.SetData(image, &config.ImageConfig{Substrate: "continuation-test", Kind: "rootfs", Source: "/base", Architecture: "amd64", GuestFamily: "linux"}))
			st.Resources = append(st.Resources, state.Resource{Address: image, Driver: "continuation-test", OwnerUID: st.TopologyUID, ResourceUID: "image", Generation: 1, Attributes: state.MustAttributes(map[string]any{"image_id": "base", "kind": "rootfs", "source": "/base", "sha256": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "architecture": "amd64", "guest_family": "linux"})})
			for _, name := range []string{"first", "second", "third"} {
				addr := address.Resource("sysbox_node", name)
				require.NoError(t, g.AddNode(addr, []address.Address{image}))
				require.NoError(t, g.SetData(addr, &config.NodeConfig{Substrate: "continuation-test", Image: "sysbox_image.base.id"}))
				st.Resources = append(st.Resources, state.Resource{Address: addr, Driver: "continuation-test", ExternalID: "old-" + name, OwnerUID: st.TopologyUID, ResourceUID: name, Generation: 1, Attributes: state.MustAttributes(map[string]any{"guest_family": "linux"})})
			}
			require.NoError(t, mgr.Save(st))
			plan, err := runtime.BuildResetPlan(g, st, "")
			require.NoError(t, err)
			queued := time.Now().UTC()
			originalPath := filepath.Join(t.TempDir(), "original.checkpoint.json")
			original := runtime.NewExecutor(g, st)
			original.SetRunContext("lab", "original")
			original.SetRunIncarnation(queued, "config")
			recorder := runtime.NewFileRecorder(originalPath, "original", "lab")
			recorder.SetStateSerialBefore(st.Meta.Serial)
			original.SetRecorder(recorder)
			captured := &captureResetBatch{Repository: r}
			original.SetActionRepository(captured)
			require.ErrorContains(t, original.Reset(ctx, plan), "capture original approval")
			// Reproduce the retained historical incident: two original actions
			// reached destroyed; the third was never admitted.
			for _, action := range captured.batch[:2] {
				require.NoError(t, r.CommitActionIntent(ctx, action))
				action.Revision = 1
				handle, err := d.PrepareReset(ctx, substrate.ResetRequest{Current: substrate.NodeHandle{ID: action.Candidate.ExternalID}})
				require.NoError(t, err)
				token, err := d.MarshalResetHandle(handle)
				require.NoError(t, err)
				require.NoError(t, action.Candidate.SetRuntimeValue("reset_handle", string(token)))
				for _, phase := range []string{"preparing", "prepared", "destroying", "destroy_returned", "destroyed"} {
					if phase == "destroy_returned" {
						require.NoError(t, d.DestroyReset(ctx, handle))
					}
					action.Phase, action.Status = phase, runtime.ActionUncertain
					require.NoError(t, r.CommitActionOutcome(ctx, action, nil))
					action.Revision++
				}
			}
			checkpointBytes, err := os.ReadFile(originalPath)
			require.NoError(t, err)
			cp, err := runtime.LoadCheckpointFile(originalPath)
			require.NoError(t, err)
			for _, executionID := range []string{"continuation", "cold-retry"} {
				current, err := r.LoadState(ctx, "lab")
				require.NoError(t, err)
				e := runtime.NewExecutor(g, current)
				e.SetRunContext("lab", executionID)
				e.SetRecorder(runtime.NewFileRecorder(filepath.Join(t.TempDir(), executionID+".json"), executionID, "lab"))
				e.SetActionRepository(r)
				req, err := e.PrepareResetContinuation(ctx, cp, queued, "config")
				require.NoError(t, err)
				require.NoError(t, e.ContinueReset(ctx, *req))
			}
			require.Equal(t, 3, d.destroyCalls, "two historical destroys must not be repeated")
			require.Equal(t, 3, d.applyCalls, "cold retry must not recreate completed nodes")
			actions, err := r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Len(t, actions, 3)
			for _, action := range actions {
				require.Equal(t, "original", action.RunID)
				require.Equal(t, int64(2), action.Generation)
				require.Equal(t, runtime.ActionSucceeded, action.Status)
			}
			for _, originalAction := range captured.batch[:2] {
				found := false
				for _, action := range actions {
					if action.ID == originalAction.ID && action.ResourceUID == originalAction.ResourceUID {
						found = true
					}
				}
				require.True(t, found, "original action and resource IDs must survive continuation")
			}
			unchanged, err := os.ReadFile(originalPath)
			require.NoError(t, err)
			require.Equal(t, checkpointBytes, unchanged)
		})
	}
}
