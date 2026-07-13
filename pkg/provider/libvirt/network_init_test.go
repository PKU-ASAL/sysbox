package libvirt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/substrate"
)

func TestBuildNoCloudUserDataIncludesAuthorizedKey(t *testing.T) {
	data, err := buildNoCloudUserData("sysbox", "ssh-ed25519 AAAATEST matrix")
	require.NoError(t, err)
	require.Contains(t, string(data), "#cloud-config")
	require.Contains(t, string(data), "name: sysbox")
	require.Contains(t, string(data), "ssh-ed25519 AAAATEST matrix")
	require.Contains(t, string(data), "NOPASSWD")
}

func TestGuestNetworkProviderStateOmitsResolvedCredentials(t *testing.T) {
	hs := &HandleState{
		DomainName: "vm", NetworkInit: substrate.GuestNetworkInitCloudInit, SeedISO: "/tmp/seed.iso",
		SSHPass: "plaintext-password", SSHAuthorizedKey: "ssh-ed25519 plaintext-public-key",
	}
	raw, err := New().MarshalProviderState(substrate.NodeHandle{ID: "vm", Provider: hs})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "plaintext-password")
	require.NotContains(t, string(raw), "plaintext-public-key")
	require.Contains(t, string(raw), "cloud_init")
	require.Contains(t, string(raw), "seed.iso")

	restored, err := New().UnmarshalProviderState(raw)
	require.NoError(t, err)
	got := restored.(*HandleState)
	require.Equal(t, substrate.GuestNetworkInitCloudInit, got.NetworkInit)
	require.True(t, strings.HasSuffix(got.SeedISO, "seed.iso"))
}

func TestPrepareGuestNetworkPreconfiguredSkipsSeed(t *testing.T) {
	hs := &HandleState{NetworkInit: substrate.GuestNetworkInitPreconfigured, VMDir: t.TempDir(), DomainName: "vm"}
	err := New().PrepareGuestNetwork(context.Background(), substrate.NodeHandle{ID: "vm", Provider: hs})
	require.NoError(t, err)
	require.Empty(t, hs.SeedISO)
}

func TestObserveGuestNetworkConvergesIPv4(t *testing.T) {
	previousProbe, previousTimeout, previousInterval := guestNetworkProbe, guestNetworkReadyTimeout, guestNetworkPollInterval
	guestNetworkProbe = func(_ context.Context, namespace, ip string) error {
		require.Equal(t, "matrix-ns", namespace)
		require.Equal(t, "10.44.0.30", ip)
		return nil
	}
	guestNetworkReadyTimeout = time.Second
	guestNetworkPollInterval = time.Millisecond
	t.Cleanup(func() {
		guestNetworkProbe, guestNetworkReadyTimeout, guestNetworkPollInterval = previousProbe, previousTimeout, previousInterval
	})
	hs := &HandleState{NetworkInit: substrate.GuestNetworkInitCloudInit, Bridges: []BridgeAttach{{Name: "matrix", Netns: "matrix-ns", MAC: "02:00:00:00:00:30", IPPrefixes: []string{"10.44.0.30/24"}}}}

	observation, err := New().ObserveGuestNetwork(context.Background(), substrate.NodeHandle{ID: "vm", Provider: hs})

	require.NoError(t, err)
	require.True(t, observation.Converged)
	require.Len(t, observation.Interfaces, 1)
	require.True(t, observation.Interfaces[0].Converged)
}

func TestObserveGuestNetworkReportsFailureAndRejectsIPv6(t *testing.T) {
	previousProbe, previousTimeout, previousInterval := guestNetworkProbe, guestNetworkReadyTimeout, guestNetworkPollInterval
	guestNetworkProbe = func(context.Context, string, string) error { return errors.New("unreachable") }
	guestNetworkReadyTimeout = 2 * time.Millisecond
	guestNetworkPollInterval = time.Millisecond
	t.Cleanup(func() {
		guestNetworkProbe, guestNetworkReadyTimeout, guestNetworkPollInterval = previousProbe, previousTimeout, previousInterval
	})
	hs := &HandleState{NetworkInit: substrate.GuestNetworkInitPreconfigured, Bridges: []BridgeAttach{{Name: "matrix", Netns: "matrix-ns", MAC: "02:00:00:00:00:30", IPPrefixes: []string{"10.44.0.30/24"}}}}

	observation, err := New().ObserveGuestNetwork(context.Background(), substrate.NodeHandle{ID: "vm", Provider: hs})
	require.NoError(t, err)
	require.False(t, observation.Converged)
	require.Contains(t, observation.Interfaces[0].Reason, "unreachable")

	hs.Bridges[0].IPPrefixes = []string{"2001:db8::30/64"}
	_, err = New().ObserveGuestNetwork(context.Background(), substrate.NodeHandle{ID: "vm", Provider: hs})
	require.ErrorContains(t, err, "IPv6")
}
