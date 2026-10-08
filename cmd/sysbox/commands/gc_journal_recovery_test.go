package commands

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGCJournalRejectsUncertainReplayAndChangedCandidate(t *testing.T) {
	for _, status := range []string{"intent", "uncertain", "complete"} {
		t.Run(status, func(t *testing.T) {
			root, owner := gcInventoryFixture(t)
			active, err := collectActiveIDsChecked(root)
			require.NoError(t, err)
			candidate := orphan{Kind: "unsupported", ID: "external", OwnerUID: owner, ResourceUID: "resource", Generation: 1, InventoryDigest: active.inventories[owner]}
			path := filepath.Join(root, "_gc", "deletion-journal.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			prior := gcJournalRecord{Candidate: candidate, Digest: gcCandidateDigest(candidate), Status: status}
			require.NoError(t, saveGCJournal(path, gcJournal{Version: 1, Records: map[string]gcJournalRecord{"unsupported:external": prior}}))
			if status == "complete" {
				candidate.Generation++
			}
			err = executeOrphans(context.Background(), root, []orphan{candidate}, io.Discard)
			if status == "complete" {
				require.ErrorContains(t, err, "identity changed")
			} else {
				require.ErrorContains(t, err, "outcome uncertain")
			}
			journal, err := loadGCJournal(path)
			require.NoError(t, err)
			require.Equal(t, prior, journal.Records["unsupported:external"], "recovery must preserve the original evidence")
		})
	}
}
