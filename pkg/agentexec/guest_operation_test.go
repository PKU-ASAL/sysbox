package agentexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/oslab/sysbox/pkg/address"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/state"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

type guestTestDriver struct {
	req           substrate.ExecRequest
	waitForCancel bool
}

func (d *guestTestDriver) ExecInNode(ctx context.Context, _ substrate.NodeHandle, req substrate.ExecRequest) (substrate.ExecResult, error) {
	d.req = req
	if d.waitForCancel {
		<-ctx.Done()
		return substrate.ExecResult{}, ctx.Err()
	}
	return substrate.ExecResult{Stdout: "out", Stderr: "err", ExitCode: 7}, nil
}
func (*guestTestDriver) ExecBackground(context.Context, substrate.NodeHandle, substrate.ExecRequest) (int, error) {
	return 0, nil
}

type guestCodec struct{}

func (guestCodec) MarshalProviderState(substrate.NodeHandle) (json.RawMessage, error) {
	return json.RawMessage(`{"id":"opaque"}`), nil
}
func (guestCodec) UnmarshalProviderState(json.RawMessage) (any, error) { return "opaque", nil }

func guestState(t *testing.T, name string) *state.State {
	t.Helper()
	st := &state.State{}
	r := state.Resource{Address: address.Resource("sysbox_node", "web"), Driver: name}
	require.NoError(t, r.SetProviderState(json.RawMessage(`{"id":"opaque"}`)))
	st.Resources = append(st.Resources, r)
	return st
}

func TestGuestOperationMapsRequestAndReturnsStructuredResult(t *testing.T) {
	old := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = old })
	d := &guestTestDriver{}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "guest-test", Version: "1", GuestExec: d, NodeState: guestCodec{}}))
	result := executeGuestOperation(context.Background(), guestState(t, "guest-test"), "web", controlplane.GuestExecutionRequest{
		Argv: []string{"printf", "%s", "ok"}, Environment: map[string]string{"TOKEN": "secret"}, WorkingDirectory: "/work",
	})
	require.Equal(t, "printf", d.req.Program)
	require.Equal(t, []string{"%s", "ok"}, d.req.Args)
	require.Equal(t, map[string]string{"TOKEN": "secret"}, d.req.Environment)
	require.Equal(t, "/work", d.req.WorkingDir)
	require.Equal(t, 7, result.Result.ExitCode)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("out")), result.Result.Stdout)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("err")), result.Result.Stderr)
	require.Equal(t, "base64", result.Result.Encoding)
}

func TestGuestOperationTimeoutCancelsProvider(t *testing.T) {
	old := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = old })
	d := &guestTestDriver{waitForCancel: true}
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "guest-test", Version: "1", GuestExec: d, NodeState: guestCodec{}}))
	started := time.Now()
	result := executeGuestOperation(context.Background(), guestState(t, "guest-test"), "web", controlplane.GuestExecutionRequest{Argv: []string{"sleep", "10"}, TimeoutSeconds: 1})
	require.Less(t, time.Since(started), 2*time.Second)
	require.Equal(t, "timeout", result.ResultClass)
	require.NotContains(t, result.Err, "secret")
}

func TestGuestOperationMissingCapabilityIsClassified(t *testing.T) {
	old := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = old })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "guest-test", Version: "1", NodeState: guestCodec{}}))
	result := executeGuestOperation(context.Background(), guestState(t, "guest-test"), "web", controlplane.GuestExecutionRequest{Argv: []string{"true"}})
	require.Equal(t, "unsupported", result.ResultClass)
}
