package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHostObservationBindsSessionSequenceAndContents(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	o := HostObservation{AgentID: "agent", SessionID: "process", Sequence: 1, BootID: "boot", ObservedAt: time.Now().UTC(), Inventory: AgentInventory{AgentID: "agent", Capabilities: []string{"docker"}}}
	signed, err := SignHostObservation(private, o)
	require.NoError(t, err)
	require.NoError(t, VerifyHostObservation(public, signed))
	for _, mutate := range []func(*HostObservation){func(o *HostObservation) { o.SessionID = "other" }, func(o *HostObservation) { o.Sequence++ }, func(o *HostObservation) { o.Inventory.Resources.CPU++ }, func(o *HostObservation) { o.AgentID = "other" }} {
		changed := signed
		mutate(&changed)
		require.Error(t, VerifyHostObservation(public, changed))
	}
	signed.Signature = nil
	require.Error(t, VerifyHostObservation(public, signed))
}
