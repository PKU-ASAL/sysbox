package commands

import (
	"bytes"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoricalAuditReadsLegacyJournalWithoutMigration(t *testing.T) {
	for _, format := range []int{4, 5, 6} {
		t.Run(string(rune('0'+format)), func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"format": format, "state": map[string]any{"topology_uid": "owner"}, "actions": map[string]any{"old": map[string]any{"id": "old", "run_id": "run", "status": "uncertain", "candidate": map[string]any{"external_id": "container", "private": map[string]any{"secret": "do-not-expose"}}}}})
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "journal.json")
			require.NoError(t, os.WriteFile(path, raw, 0600))
			cmd := newHistoricalAuditCommand()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{path})
			require.NoError(t, cmd.Execute())
			require.Contains(t, out.String(), "source_sha256")
			require.Contains(t, out.String(), "manual_reconciliation_required")
			require.NotContains(t, out.String(), "do-not-expose")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, raw, after)
		})
	}
}

func TestHistoricalAuditReadsHostIntentWithoutBackfillingAdmission(t *testing.T) {
	raw := []byte(`{"version":2,"topology_uid":"owner","outcomes":{"30/run":{"grant":{"action_id":"run","operation":"run_destroy","target_boot_id":"old-boot"},"state":"intent","result":null}}}`)
	path := filepath.Join(t.TempDir(), "host.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	cmd := newHistoricalAuditCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{path})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "old-boot")
	require.Contains(t, out.String(), "missing_admission_evidence")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, raw, after)
}
