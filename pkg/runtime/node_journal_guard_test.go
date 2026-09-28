package runtime

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type nodeGuardDriver struct {
	*portTestSubstrate
	conn                               *recordingConn
	creates, starts, destroys, entries int
	startErr, prepareErr, routeErr     error
	onDestroy                          func()
}

func (d *nodeGuardDriver) CreateNode(ctx context.Context, spec substrate.NodeSpec) (substrate.NodeHandle, error) {
	d.creates++
	handle, err := d.portTestSubstrate.CreateNode(ctx, spec)
	handle.ID = spec.Name
	return handle, err
}
func (d *nodeGuardDriver) StartNode(context.Context, substrate.NodeHandle) error {
	d.starts++
	return d.startErr
}
func (d *nodeGuardDriver) DestroyNode(context.Context, substrate.NodeHandle) error {
	d.destroys++
	if d.onDestroy != nil {
		d.onDestroy()
	}
	return nil
}
func (d *nodeGuardDriver) PrepareHandle(_ context.Context, h *substrate.NodeHandle, _ any, _ substrate.StateReader) error {
	if h.ID != "" {
		return d.prepareErr
	}
	return nil
}
func (d *nodeGuardDriver) Connection(substrate.NodeHandle, []substrate.ConnectionHint) (substrate.Connection, error) {
	return d.conn, nil
}
func (d *nodeGuardDriver) ExecImageEntry(context.Context, substrate.NodeHandle) error {
	d.entries++
	return nil
}
func (d *nodeGuardDriver) EnsureRoute(context.Context, substrate.NodeHandle, string, string) error {
	return d.routeErr
}

type nodePhaseFailure struct {
	phase  string
	status OperationStatus
	err    error
}

func (f nodePhaseFailure) SaveCheckpoint(_ context.Context, _, _ string, checkpoint OperationCheckpoint) error {
	for _, step := range checkpoint.Steps {
		if step.Phase == f.phase && step.Status == f.status {
			return f.err
		}
	}
	return nil
}

func nodeGuardExecutor(t *testing.T, failure *nodePhaseFailure) (*Executor, *nodeGuardDriver, *config.NodeConfig, address.Address) {
	t.Helper()
	d := &nodeGuardDriver{portTestSubstrate: &portTestSubstrate{name: "node-guard"}, conn: &recordingConn{}}
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: d.name, Version: "test", Node: d, NIC: d, NodeState: d, GuestNetwork: d, GuestNetworkInit: d, ImageEntry: d}))
	g := graph.New()
	addr := address.Resource("sysbox_node", "first")
	cfg := &config.NodeConfig{Image: "sysbox_image.base.id", Substrate: d.name}
	require.NoError(t, g.AddNode(addr, nil))
	require.NoError(t, g.SetData(addr, cfg))
	e := NewExecutor(g, &state.State{Version: state.SchemaVersion})
	e.SetLogger(io.Discard)
	e.state.AddResource(state.Resource{Address: address.Resource("sysbox_image", "base"), Driver: d.name, Attributes: map[string]any{"image_id": "image", "repository": "base", "guest_family": "linux"}})
	if failure != nil {
		path := filepath.Join(t.TempDir(), "checkpoint.json")
		e.SetRecorder(NewStoreRecorder(NewFileRecorder(path, "run", "lab"), *failure, "lab", "run", path))
	}
	return e, d, cfg, addr
}

func TestNodeJournalFailureDoesNotInvokeCleanup(t *testing.T) {
	for _, phase := range []string{"start_node", "attach", "prepare_guest_network", "cold_start"} {
		for _, status := range []OperationStatus{OperationStarted, OperationDone} {
			t.Run(phase+"/"+string(status), func(t *testing.T) {
				want := errors.New("node phase journal failed")
				target := phase
				if phase == "cold_start" {
					target = "start_node"
				}
				e, d, cfg, addr := nodeGuardExecutor(t, &nodePhaseFailure{phase: target, status: status, err: want})
				d.coldPlug = phase == "cold_start"
				if phase == "attach" {
					e.state.AddResource(state.Resource{Address: address.Resource("sysbox_network", "lan"), Attributes: map[string]any{"cidr": "10.0.0.0/24"}})
					cfg.Links = []config.LinkConfig{{Name: "lan", Network: "sysbox_network.lan", IP: "10.0.0.2/24"}}
				}
				if phase == "prepare_guest_network" {
					d.guestInitModes = []substrate.GuestNetworkInitMode{substrate.GuestNetworkInitCloudInit}
				}
				err := e.Apply(context.Background(), &Plan{Actions: []controlplane.PlannedChange{{Address: addr, Action: controlplane.PlanActionCreate}}})
				require.ErrorIs(t, err, want)
				require.Equal(t, 1, d.creates)
				require.Zero(t, d.destroys, "a journal failure must not trigger a destructive callback")
			})
		}
	}
}

func TestNodeWarningDoesNotContinueAfterJournalFailure(t *testing.T) {
	for _, phase := range []string{"prepare_connection", "attach_route"} {
		for _, status := range []OperationStatus{OperationStarted, OperationDone} {
			t.Run(phase+"/"+string(status), func(t *testing.T) {
				want := errors.New("node warning journal failed")
				e, d, cfg, addr := nodeGuardExecutor(t, &nodePhaseFailure{phase: phase, status: status, err: want})
				cfg.Routes = []config.RouteConfig{{Destination: "10.0.1.0/24", Via: "10.0.0.1"}}
				cfg.Provisioners = []config.ProvisionerConfig{{Type: "file", Source: "source", Destination: "destination"}}
				err := e.Apply(context.Background(), &Plan{Actions: []controlplane.PlannedChange{{Address: addr, Action: controlplane.PlanActionCreate}}})
				require.ErrorIs(t, err, want)
				require.Empty(t, d.conn.copiedSrc, "provisioners must not run after a journal failure")
				require.Zero(t, d.entries, "image entry must not run after a journal failure")
				require.Zero(t, d.destroys)
			})
		}
	}
}

func TestNodeOperationalCleanupAndWarningsRemainUnchanged(t *testing.T) {
	t.Run("start failure cleans up", func(t *testing.T) {
		e, d, _, addr := nodeGuardExecutor(t, nil)
		want := errors.New("provider start failure")
		d.startErr = want
		require.ErrorIs(t, e.Apply(context.Background(), &Plan{Actions: []controlplane.PlannedChange{{Address: addr, Action: controlplane.PlanActionCreate}}}), want)
		require.Equal(t, 1, d.destroys)
	})
	t.Run("ordinary warnings continue", func(t *testing.T) {
		e, d, cfg, addr := nodeGuardExecutor(t, nil)
		d.prepareErr, d.routeErr = errors.New("prepare warning"), errors.New("route warning")
		cfg.Routes = []config.RouteConfig{{Destination: "10.0.1.0/24", Via: "10.0.0.1"}}
		cfg.Provisioners = []config.ProvisionerConfig{{Type: "file", Source: "source", Destination: "destination"}}
		require.NoError(t, e.Apply(context.Background(), &Plan{Actions: []controlplane.PlannedChange{{Address: addr, Action: controlplane.PlanActionCreate}}}))
		require.Equal(t, "source", d.conn.copiedSrc)
		require.Equal(t, 1, d.entries)
	})
}

func TestRollbackStopsWhenEarlierDeletionBreaksJournal(t *testing.T) {
	want := errors.New("rollback journal failed")
	e, d, cfg, first := nodeGuardExecutor(t, &nodePhaseFailure{phase: "rollback", status: OperationStarted, err: want})
	second := address.Resource("sysbox_node", "second")
	require.NoError(t, e.graph.AddNode(second, nil))
	require.NoError(t, e.graph.SetData(second, cfg))
	d.onDestroy = func() { e.recorder.SubstepStart(-1, "rollback", nil) }
	err := e.Apply(context.Background(), &Plan{Actions: []controlplane.PlannedChange{
		{Address: first, Action: controlplane.PlanActionCreate},
		{Address: second, Action: controlplane.PlanActionCreate},
		{Address: address.Resource("sysbox_node", "unknown"), Action: controlplane.PlanActionUnknown},
	}})
	require.ErrorIs(t, err, want)
	require.Equal(t, 2, d.creates)
	require.Equal(t, 1, d.destroys, "rollback must recheck the journal before each provider deletion")
	require.NotNil(t, e.state.FindResource(first))
}
