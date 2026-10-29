package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/oslab/sysbox/pkg/model"
	"github.com/oslab/sysbox/pkg/state"
)

func TestFenceObservationSignatureBindsEntireReportAndDelivery(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	binding := FenceObservationBinding{RequestSHA256: strings.Repeat("a", 64), HostBootID: "11111111-1111-4111-8111-111111111111"}
	report := RunRecoveryReport{CommandID: "observe", AgentID: "agent", RunID: "run", RunAttempt: 1, RunLeaseOwner: "run-owner", QueuedAt: time.Now().UTC(), Revision: "revision", Mode: RunRecoveryObserve, TopologyUID: "topology", StateSerial: 2, ObservedAt: time.Now().UTC(), Resources: []model.RecoveredResource{{ActionID: "action", OwnerUID: "topology", ResourceUID: "node", Generation: 1, Resource: "sysbox_node.web", Status: "unknown", JournalStatus: "uncertain"}}}
	report.Resources[0].Discovered = &state.Resource{Attachments: []state.Attachment{{DriverState: json.RawMessage(`{"long_key":9007199254740993,"a":true,"exponent":1e3}`)}}}
	signed, err := SignFenceObservation(key, report, binding, "worker", 3)
	require.NoError(t, err)
	require.NoError(t, VerifyFenceObservation(pub, signed, binding, "worker", 3))
	reformatted := signed
	reformatted.Resources = append([]model.RecoveredResource(nil), signed.Resources...)
	copyResource := *signed.Resources[0].Discovered
	copyResource.Attachments = append([]state.Attachment(nil), copyResource.Attachments...)
	copyResource.Attachments[0].DriverState = json.RawMessage(`{"a": true, "long_key": 9007199254740993,"exponent":1000}`)
	reformatted.Resources[0].Discovered = &copyResource
	require.NoError(t, VerifyFenceObservation(pub, reformatted, binding, "worker", 3), "JSONB field order must not invalidate exact provider evidence")
	for _, change := range []func(*RunRecoveryReport){func(r *RunRecoveryReport) { r.Resources[0].Status = "absent" }, func(r *RunRecoveryReport) { r.CommandID = "other" }, func(r *RunRecoveryReport) { r.StateSerial++ }, func(r *RunRecoveryReport) {
		r.HostObservation.Binding.HostBootID = "22222222-2222-4222-8222-222222222222"
	}, func(r *RunRecoveryReport) { r.HostObservation.Binding.RequestSHA256 = strings.Repeat("b", 64) }, func(r *RunRecoveryReport) { r.HostObservation.DeliveryAttempt++ }} {
		copy := signed
		proof := *signed.HostObservation
		copy.HostObservation = &proof
		copy.Resources = append([]model.RecoveredResource(nil), signed.Resources...)
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

func TestFenceObservationVersionOneKeepsOriginalWire(t *testing.T) {
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("x", 32)))
	binding := FenceObservationBinding{RequestSHA256: strings.Repeat("a", 64), HostBootID: "11111111-1111-4111-8111-111111111111"}
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	r := RunRecoveryReport{HostObservation: &HostObservationProof{Version: 1, Binding: binding, DeliveryOwner: "worker", DeliveryAttempt: 1}, CommandID: "observe", AgentID: "agent", RunID: "run", RunAttempt: 1, QueuedAt: at, Revision: "revision", Mode: RunRecoveryObserve, TopologyUID: "owner", ObservedAt: at, Resources: []model.RecoveredResource{{ActionID: "a", Discovered: &state.Resource{Attachments: []state.Attachment{{DriverState: json.RawMessage(`{"z":1e3,"a":9007199254740993}`)}}}}}}
	type legacyReport RunRecoveryReport
	original, err := json.Marshal(legacyReport(r))
	require.NoError(t, err)
	r.HostObservation.Signature = ed25519.Sign(key, append([]byte("sysbox.fence-observation.v1\x00"), original...))
	require.NoError(t, VerifyFenceObservation(key.Public().(ed25519.PublicKey), r, binding, "worker", 1))
	wire, err := json.Marshal(r)
	require.NoError(t, err)
	legacy, err := json.Marshal(legacyReport(r))
	require.NoError(t, err)
	require.Equal(t, legacy, wire)
}
