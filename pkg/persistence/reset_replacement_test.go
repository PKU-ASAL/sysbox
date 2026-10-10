package persistence

import (
	"context"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResetReplacementPreservesOldEvidenceUntilCompletion(t *testing.T) {
	for _, kind := range []string{"filesystem", "sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) {
			r := resetTestRepository(t, kind)
			req := continuationFixture(t, r)
			a := req.Existing[0] // destroyed, old generation 1
			replacement := *a.Candidate
			replacement.Generation, replacement.ExternalID = a.Generation, "new-node"
			a.Replacement = &replacement
			ctx := context.Background()
			require.ErrorIs(t, r.CommitActionOutcome(ctx, a, nil), ErrCorruptRecord, "cannot publish before applying")
			a.Phase = "applying"
			require.NoError(t, r.CommitActionOutcome(ctx, a, nil))
			a.Revision++
			for _, corruption := range []string{"discard", "substitute", "generation", "completed-different"} {
				bad := a
				copy := replacement
				bad.Replacement = &copy
				switch corruption {
				case "discard":
					bad.Replacement = nil
				case "substitute":
					copy.ExternalID = "foreign"
				case "generation":
					copy.Generation++
				case "completed-different":
					bad.Phase, bad.EffectComplete = "verifying", true
					other := replacement
					other.ExternalID = "foreign"
					bad.Candidate = &other
				}
				require.Error(t, r.CommitActionOutcome(ctx, bad, nil), corruption)
			}
			loaded, err := r.LoadActions(ctx, "lab")
			require.NoError(t, err)
			require.Contains(t, loaded, a)
			require.False(t, a.ResetContinuationSafe(), "routing evidence does not permit replay of interrupted setup")
			a.Phase, a.EffectComplete, a.Candidate = "verifying", true, &replacement
			a.Status = runtime.ActionUncertain
			require.NoError(t, r.CommitActionOutcome(ctx, a, nil))
		})
	}
}
