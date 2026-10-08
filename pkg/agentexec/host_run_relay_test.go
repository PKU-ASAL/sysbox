package agentexec

import (
	"encoding/json"
	"testing"

	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
)

func TestDecodeHostRunEvidenceAcceptsStartEnvelopeAndObservePayload(t *testing.T) {
	run := controlplane.Run{ID: "run-1", Status: controlplane.RunDone}
	raw, err := json.Marshal(struct {
		Result controlplane.Run `json:"result"`
	}{Result: run})
	require.NoError(t, err)
	got, err := decodeHostRunEvidence(raw)
	require.NoError(t, err)
	require.Equal(t, run.ID, got.ID)

	raw, err = json.Marshal(run)
	require.NoError(t, err)
	got, err = decodeHostRunEvidence(raw)
	require.NoError(t, err)
	require.Equal(t, run.ID, got.ID)
}

func TestDecodeHostRunEvidenceRejectsNonTerminalEvidence(t *testing.T) {
	raw, err := json.Marshal(controlplane.Run{ID: "run-1", Status: controlplane.RunRunning})
	require.NoError(t, err)
	_, err = decodeHostRunEvidence(raw)
	require.Error(t, err)
}
