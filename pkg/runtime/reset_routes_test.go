package runtime

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type resetRouteDriver struct {
	*portTestSubstrate
	installed                     bool
	probeErr                      error
	omitRoute                     bool
	ensureCalls, provisionerCalls int
}

func (d *resetRouteDriver) EnsureRoute(context.Context, substrate.NodeHandle, string, string) error {
	d.ensureCalls++
	if !d.omitRoute {
		d.installed = true
	}
	return nil
}
func (d *resetRouteDriver) HasRoute(_ context.Context, h substrate.NodeHandle, _, _ string) (bool, error) {
	if h.ID != "node-reset" {
		return true, nil
	}
	if d.probeErr != nil {
		return false, d.probeErr
	}
	return d.installed, nil
}
func (d *resetRouteDriver) Connection(substrate.NodeHandle, []substrate.ConnectionHint) (substrate.Connection, error) {
	return resetRouteConnection{d}, nil
}

type resetRouteConnection struct{ driver *resetRouteDriver }

func (c resetRouteConnection) Exec(context.Context, substrate.ExecRequest, io.Writer, io.Writer) (substrate.ExecResult, error) {
	c.driver.provisionerCalls++
	if !c.driver.installed {
		return substrate.ExecResult{}, errors.New("provisioner ran before route was restored")
	}
	return substrate.ExecResult{ExitCodeKnown: true, Status: substrate.ExecStatusExited}, nil
}

func resetRouteFixture(t *testing.T) (*Executor, *resetRouteDriver, *Plan) {
	t.Helper()
	d := &resetRouteDriver{portTestSubstrate: &portTestSubstrate{name: "reset-routes", coldPlug: true}}
	previous := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = previous })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: d.name, Version: "test", Node: d, NIC: d, NodeState: d, GuestNetwork: d, GuestNetworkInit: d, Reset: d}))
	g, st := configuredResetTestState(t, d.portTestSubstrate, "first")
	id := address.Resource("sysbox_node", "first")
	cfg := g.Get(id).Data.(*config.NodeConfig)
	cfg.Routes = []config.RouteConfig{{Destination: "10.20.0.0/16", Via: "10.0.0.1"}}
	cfg.Provisioners = []config.ProvisionerConfig{{Type: "exec", Program: "service", Shell: "none"}}
	require.NoError(t, st.FindResource(id).SetAttribute("routes", []map[string]string{{"dst": "10.20.0.0/16", "via": "10.0.0.1"}}))
	e := testActionExecutor(t, g, st)
	plan, err := BuildResetPlan(g, st, id.String())
	require.NoError(t, err)
	return e, d, plan
}
func TestResetRestoresDeclaredRoutesBeforeProvisioners(t *testing.T) {
	e, d, plan := resetRouteFixture(t)
	require.NoError(t, e.Reset(context.Background(), plan))
	require.Equal(t, 1, d.ensureCalls)
	require.Equal(t, 1, d.provisionerCalls)
	require.Equal(t, ActionSucceeded, e.actions.(*memoryActions).actions[0].Status)
}
func TestResetUnknownRouteObservationBlocksMutationAndProvisioners(t *testing.T) {
	e, d, plan := resetRouteFixture(t)
	d.probeErr = errors.New("route query timed out")
	require.ErrorIs(t, e.Reset(context.Background(), plan), ErrObservationUnknown)
	require.Zero(t, d.ensureCalls)
	require.Zero(t, d.provisionerCalls)
	require.Equal(t, ActionUncertain, e.actions.(*memoryActions).actions[0].Status)
}
func TestResetRouteRequiresPersistedSubstepIntent(t *testing.T) {
	e, d, plan := resetRouteFixture(t)
	want := errors.New("route intent unavailable")
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	e.SetRecorder(NewStoreRecorder(NewFileRecorder(path, e.runID, e.topology), nodePhaseFailure{phase: "attach_route", status: OperationStarted, err: want}, e.topology, e.runID, path))
	require.ErrorIs(t, e.Reset(context.Background(), plan), want)
	require.Zero(t, d.ensureCalls)
	require.Zero(t, d.provisionerCalls)
}
func TestResetRouteVerificationFailureBlocksProvisioners(t *testing.T) {
	e, d, plan := resetRouteFixture(t)
	d.omitRoute = true
	require.ErrorIs(t, e.Reset(context.Background(), plan), ErrVerification)
	require.Equal(t, 1, d.ensureCalls)
	require.Zero(t, d.provisionerCalls)
	require.Equal(t, ActionUncertain, e.actions.(*memoryActions).actions[0].Status)
}
