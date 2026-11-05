//go:build e2e

package docker

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

// Run only against an explicitly selected disposable daemon. It proves the
// gap the platform hit: a background provisioner on a stock OCI image starts,
// is observed and is stopped through the daemon, with no guest-side helper.
func TestDockerBackgroundProvisionerE2E(t *testing.T) {
	if os.Getenv("SYSBOX_DOCKER_BACKGROUND_E2E") != "1" {
		t.Skip("set SYSBOX_DOCKER_BACKGROUND_E2E=1 to run against a disposable Docker daemon")
	}
	require.NotEmpty(t, os.Getenv("DOCKER_HOST"))

	sub, err := New()
	require.NoError(t, err)
	defer sub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	image, err := sub.ResolveImage(ctx, substrate.ArtifactSource{
		Kind: substrate.ArtifactOCI, Source: "alpine:3.22", Architecture: "amd64", GuestFamily: substrate.GuestFamilyLinux,
	})
	require.NoError(t, err)
	handle, err := sub.CreateNode(substrate.WithOperationIdentity(ctx, testOperation()), substrate.NodeSpec{
		Name:  "sysbox-background-e2e",
		Image: image,
	})
	require.NoError(t, err)
	defer func() { _ = sub.DestroyNode(context.Background(), handle) }()
	require.NoError(t, sub.StartNode(ctx, handle))

	conn, err := sub.Connection(handle, nil)
	require.NoError(t, err)
	starter, ok := conn.(substrate.ConnectionStarter)
	require.True(t, ok)

	execution, err := starter.Start(ctx, substrate.ExecRequest{Program: "sleep", Args: []string{"60"}, Shell: substrate.ShellNone})
	require.NoError(t, err)
	require.NotEmpty(t, execution.ID)
	require.NotZero(t, execution.PID)

	observer, ok := conn.(substrate.ConnectionExecutionObserver)
	require.True(t, ok)
	observed, err := observer.ObserveExecution(ctx, execution)
	require.NoError(t, err)
	require.Equal(t, substrate.ExecStatusRunning, observed.Status)
	require.False(t, observed.ExitCodeKnown)

	canceler, ok := conn.(substrate.ConnectionExecutionCanceler)
	require.True(t, ok)
	termination, err := canceler.CancelExecution(ctx, execution)
	if os.Geteuid() != 0 {
		// The supervisor runs as root; an unprivileged developer cannot signal a
		// container process owned by root, and cancellation must say so instead
		// of pretending the task stopped.
		require.Error(t, err)
		require.Equal(t, substrate.RemoteTerminationUnknown, termination)
		t.Logf("cancellation requires root, as deployed: %v", err)
		return
	}
	require.NoError(t, err)
	require.Equal(t, substrate.RemoteTerminationConfirmed, termination)

	observed, err = observer.ObserveExecution(ctx, execution)
	require.NoError(t, err)
	require.Equal(t, substrate.ExecStatusExited, observed.Status)
	require.True(t, observed.ExitCodeKnown, "the daemon must report the final exit status")
}
