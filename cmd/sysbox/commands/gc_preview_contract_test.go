package commands

import (
	"bytes"
	"context"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestGCDoesNotExposeLegacyExecutionFlag(t *testing.T) {
	require.Nil(t, gcCmd.Flags().Lookup("execute"), "provider deletion must be admitted through the canonical API, not a CLI-local journal")
	require.ErrorContains(t, gcCmd.ParseFlags([]string{"--execute"}), "unknown flag")
}

func TestGCPreviewPreservesHistoricalDeletionEvidence(t *testing.T) {
	root, _ := gcInventoryFixture(t)
	gcFlags(t, root, true, "")
	t.Setenv("SYSBOX_PROVIDER_FIRECRACKER_WORKDIR", t.TempDir())
	gcFakeDocker(t, nil, nil)
	gcFakeVirsh(t, "exit 0\n")
	journal := filepath.Join(root, "_gc", "deletion-journal.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(journal), 0700))
	// Old uncertain records remain forensic evidence even though the old
	// replay protocol has been removed. They cannot authorize a new effect.
	raw := []byte(`{"version":1,"records":{"container:old":{"status":"uncertain"}}}`)
	require.NoError(t, os.WriteFile(journal, raw, 0600))
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&output)
	require.NoError(t, runGc(cmd, nil))
	after, err := os.ReadFile(journal)
	require.NoError(t, err)
	require.Equal(t, raw, after)
	require.Contains(t, output.String(), "nothing removed")
	entries, err := os.ReadDir(filepath.Dir(journal))
	require.NoError(t, err)
	require.Len(t, entries, 1, "preview must not create old locks or replacement journals")
}
