package controlplane

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestIntentDispositionDecisionBindsEvidenceAndOperatorChoice(t *testing.T) {
	p := IntentDispositionDecision{Version: 1, ID: "dispose", FenceID: "fence", Topology: "lab", RunID: "run", RunQueuedAt: time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC), RunRevision: "revision", ExpectedSerial: 3, JournalSHA256: repeatDigest("a"), ObservationSHA256: repeatDigest("b"), Reason: "retire interrupted operation", Actions: []IntentDispositionAction{{ActionID: "action", Decision: "retain", ResourceUID: "resource", Generation: 1, ExternalID: "external"}}}
	original, err := p.Digest()
	require.NoError(t, err)
	changed := p
	changed.Reason = "different decision"
	digest, err := changed.Digest()
	require.NoError(t, err)
	require.NotEqual(t, original, digest)
	for _, kind := range []string{"missing-fence", "missing-reason", "success", "missing-identity", "duplicate", "negative-serial", "invalid-hash", "no-actions"} {
		t.Run(kind, func(t *testing.T) {
			x := p
			x.Actions = append([]IntentDispositionAction(nil), p.Actions...)
			switch kind {
			case "missing-fence":
				x.FenceID = ""
			case "missing-reason":
				x.Reason = " "
			case "success":
				x.Actions[0].Decision = "succeeded"
			case "missing-identity":
				x.Actions[0].ExternalID = ""
			case "duplicate":
				x.Actions = append(x.Actions, x.Actions[0])
			case "negative-serial":
				x.ExpectedSerial = -1
			case "invalid-hash":
				x.JournalSHA256 = "not-digest"
			case "no-actions":
				x.Actions = nil
			}
			_, err := x.Digest()
			require.Error(t, err)
		})
	}
}
func repeatDigest(s string) string {
	var v string
	for i := 0; i < 64; i++ {
		v += s
	}
	return v
}

func TestIntentDispositionRequiresExplicitIncompleteChoice(t *testing.T) {
	p := IntentDispositionDecision{Version: 1, ID: "dispose", FenceID: "fence", Topology: "lab", RunID: "run", RunQueuedAt: time.Now().UTC(), RunRevision: "revision", JournalSHA256: repeatDigest("a"), ObservationSHA256: repeatDigest("b"), Reason: "preserve for explicit rebuild", Actions: []IntentDispositionAction{{ActionID: "a", Decision: "retain-incomplete", ResourceUID: "resource", Generation: 2, ExternalID: "replacement"}}}
	_, err := p.Digest()
	require.NoError(t, err)
	p.Actions[0].Decision = "retain-replacement-incomplete"
	_, err = p.Digest()
	require.NoError(t, err)
}
