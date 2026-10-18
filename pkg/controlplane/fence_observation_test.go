package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestFenceObservationSignatureBindsEntireReportAndDelivery(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	binding := FenceObservationBinding{RequestSHA256: strings.Repeat("a", 64), HostBootID: "11111111-1111-4111-8111-111111111111"}
	report := RunRecoveryReport{CommandID: "observe", AgentID: "agent", RunID: "run", RunAttempt: 1, RunLeaseOwner: "run-owner", QueuedAt: time.Now().UTC(), Revision: "revision", Mode: RunRecoveryObserve, TopologyUID: "topology", StateSerial: 2, ObservedAt: time.Now().UTC(), Resources: []RecoveredResource{{ActionID: "action", OwnerUID: "topology", ResourceUID: "node", Generation: 1, Resource: "sysbox_node.web", Status: "unknown", JournalStatus: "uncertain"}}}
	signed, err := SignFenceObservation(key, report, binding, "worker", 3)
	require.NoError(t, err)
	require.NoError(t, VerifyFenceObservation(pub, signed, binding, "worker", 3))
	for _, change := range []func(*RunRecoveryReport){func(r *RunRecoveryReport) { r.Resources[0].Status = "absent" }, func(r *RunRecoveryReport) { r.CommandID = "other" }, func(r *RunRecoveryReport) { r.StateSerial++ }, func(r *RunRecoveryReport) {
		r.HostObservation.Binding.HostBootID = "22222222-2222-4222-8222-222222222222"
	}, func(r *RunRecoveryReport) { r.HostObservation.Binding.RequestSHA256 = strings.Repeat("b", 64) }, func(r *RunRecoveryReport) { r.HostObservation.DeliveryAttempt++ }} {
		copy := signed
		proof := *signed.HostObservation
		copy.HostObservation = &proof
		copy.Resources = append([]RecoveredResource(nil), signed.Resources...)
		change(&copy)
		require.Error(t, VerifyFenceObservation(pub, copy, binding, "worker", 3))
	}
	require.Error(t, VerifyFenceObservation(pub, signed, binding, "worker", 4))
	require.Error(t, VerifyFenceObservation(nil, signed, binding, "worker", 3))
	require.Error(t, VerifyFenceObservation(pub, report, binding, "worker", 3))
	report.Mode = RunRecoveryRecover
	_, err = SignFenceObservation(key, report, binding, "worker", 3)
	require.Error(t, err)
}
