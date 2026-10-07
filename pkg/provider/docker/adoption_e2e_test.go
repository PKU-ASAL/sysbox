//go:build e2e

package docker

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oslab/sysbox/pkg/substrate"
	"github.com/stretchr/testify/require"
)

func TestDockerAdoptionColdLifecycleE2E(t *testing.T) {
	if os.Getenv("SYSBOX_DOCKER_ADOPTION_E2E") != "1" {
		t.Skip("requires disposable Docker container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--network", "none", "alpine:3.22", "sleep", "120").CombinedOutput()
	require.NoError(t, err, string(out))
	id := strings.TrimSpace(string(out))
	require.True(t, validAdoptionID(id))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	s, err := New()
	require.NoError(t, err)
	defer s.Close()
	s.adoptionRoot = t.TempDir()
	owner := uuid.NewString()
	identity := substrate.OperationIdentity{ActionID: "adopt", OwnerUID: owner, TopologyUID: owner, ResourceUID: uuid.NewString(), Generation: 1}
	ctx = substrate.WithOperationIdentity(ctx, identity)
	proof, err := s.InspectAdoption(ctx, id, substrate.NodeSpec{})
	require.NoError(t, err)
	h, err := s.ApplyAdoption(ctx, proof)
	require.NoError(t, err)
	private, err := s.MarshalProviderState(h)
	require.NoError(t, err)
	cold, err := New()
	require.NoError(t, err)
	defer cold.Close()
	cold.adoptionRoot = s.adoptionRoot
	decoded, err := cold.UnmarshalProviderState(private)
	require.NoError(t, err)
	h = substrate.NodeHandle{ID: id, Generation: 1, Provider: decoded}
	obs, err := cold.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.NoError(t, substrate.ValidateNodeObservationIdentity(identity, obs))
	require.True(t, obs.Running)
	wrong := identity
	wrong.Generation++
	require.Error(t, cold.Pause(substrate.WithOperationIdentity(ctx, wrong), h))
	require.NoError(t, cold.Pause(ctx, h))
	obs, err = cold.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusPaused, obs.Status)
	require.NoError(t, cold.Resume(ctx, h))
	obs, err = cold.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.Equal(t, substrate.NodeStatusRunning, obs.Status)
	require.NoError(t, cold.DestroyNode(ctx, h))
	obs, err = cold.ObserveNode(ctx, h)
	require.NoError(t, err)
	require.False(t, obs.Exists)
	t.Log("actual initially unlabelled container adopted, provider state reloaded, observed, paused/resumed, stale proof rejected and destroyed")
}
