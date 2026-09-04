package agentexec

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/driver"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

// failingGuestDriver fails an exec while optionally carrying the partial output
// the guest produced before it failed.
type failingGuestDriver struct {
	stdout string
	err    error

	// waitForCancel models the real timeout: the guest wrote output, hung, and
	// the exec returns whatever ctx.Err() reports when the deadline fires.
	waitForCancel bool
}

func (d *failingGuestDriver) ExecInNode(ctx context.Context, _ substrate.NodeHandle, _ substrate.ExecRequest) (substrate.ExecResult, error) {
	if d.waitForCancel {
		<-ctx.Done()
		return substrate.ExecResult{Stdout: d.stdout}, ctx.Err()
	}
	return substrate.ExecResult{Stdout: d.stdout}, d.err
}
func (*failingGuestDriver) ExecBackground(context.Context, substrate.NodeHandle, substrate.ExecRequest) (int, error) {
	return 0, nil
}

func registerFailingDriver(t *testing.T, d *failingGuestDriver) {
	t.Helper()
	old := driver.DefaultRegistry
	driver.DefaultRegistry = driver.NewRegistry()
	t.Cleanup(func() { driver.DefaultRegistry = old })
	require.NoError(t, driver.DefaultRegistry.Register(driver.Descriptor{Name: "guest-test", Version: "1", GuestExec: d, NodeState: guestCodec{}}))
}

// When a guest operation fails after producing output, that output is the only
// diagnostic the operator gets about how far the command went. Today it is
// dropped: the completion carries the failure class but empty stdout/stderr, so
// a timed-out check reports "timeout" with no trace of what it had already
// written. The provider captured the bytes; throwing them away one frame later
// is the exact information loss the design contract complains about.
func TestGuestOperationSurfacesPartialOutputOnFailure(t *testing.T) {
	registerFailingDriver(t, &failingGuestDriver{
		stdout: "made it this far, then hung",
		err:    errors.New("guest exec failed"),
	})

	result := executeGuestOperation(context.Background(), guestState(t, "guest-test"), "web", controlplane.GuestExecutionRequest{
		Argv: []string{"sleep", "60"},
	})

	require.Equal(t, "provider", result.ResultClass)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("made it this far, then hung")), result.Result.Stdout,
		"the partial body must reach the caller, not be discarded on failure")
}

// The timeout path is the one that actually matters for checks: whatever was
// read before the deadline fired must still be reported.
func TestGuestOperationSurfacesPartialOutputOnTimeout(t *testing.T) {
	registerFailingDriver(t, &failingGuestDriver{
		stdout:        "partial before deadline",
		waitForCancel: true,
	})

	result := executeGuestOperation(context.Background(), guestState(t, "guest-test"), "web", controlplane.GuestExecutionRequest{
		Argv: []string{"sleep", "60"}, TimeoutSeconds: 1,
	})

	require.Equal(t, "timeout", result.ResultClass)
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("partial before deadline")), result.Result.Stdout)
}

// A failure with no output is still a clean failure: empty stdout is fine, but
// it must not crash the classification or the encoding.
func TestGuestOperationFailureWithoutOutputStillClassifies(t *testing.T) {
	registerFailingDriver(t, &failingGuestDriver{err: errors.New("boom")})

	result := executeGuestOperation(context.Background(), guestState(t, "guest-test"), "web", controlplane.GuestExecutionRequest{
		Argv: []string{"true"},
	})

	require.Equal(t, "provider", result.ResultClass)
	require.Empty(t, result.Result.Stdout)
	require.Equal(t, "base64", result.Result.Encoding)
}
