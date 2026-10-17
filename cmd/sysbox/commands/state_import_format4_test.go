package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/stretchr/testify/require"
)

func TestStateImportFormat4RequiresExplicitOfflineApply(t *testing.T) {
	for _, args := range [][]string{
		{"--apply"},
		{"--apply", "--offline"},
		{"--apply", "--offline", "--source-sha256", "hash"},
	} {
		cmd := newStateImportFormat4Command()
		cmd.SetArgs(args)
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetErr(new(bytes.Buffer))
		require.ErrorContains(t, cmd.Execute(), "--offline, --source-sha256 and --archive")
	}
}

func TestStateImportFormat4PreviewAndApply(t *testing.T) {
	oldState, oldBackend := flagStateFile, flagBackend
	t.Cleanup(func() { flagStateFile, flagBackend = oldState, oldBackend })
	flagStateFile = filepath.Join(t.TempDir(), "lab", "state.json")
	flagBackend = ""
	repo, err := persistence.OpenConfigured(context.Background(), "", flagStateFile)
	require.NoError(t, err)
	defer repo.Close()
	before, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	source := filepath.Join(flagStateFile+".lifecycle", "lab.json")
	raw, err := os.ReadFile(source)
	require.NoError(t, err)
	var legacy map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &legacy))
	legacy["format"] = json.RawMessage("4")
	delete(legacy, "reset_continuations")
	delete(legacy, "gc_reservations")
	raw, err = json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(source, raw, 0600))
	preview := newStateImportFormat4Command()
	output := new(bytes.Buffer)
	preview.SetOut(output)
	preview.SetArgs(nil)
	require.NoError(t, preview.Execute())
	var plan persistence.Format4ImportPlan
	require.NoError(t, json.Unmarshal(output.Bytes(), &plan))
	require.Equal(t, before.TopologyUID, plan.TopologyUID)
	unchanged, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Equal(t, raw, unchanged)
	archive := filepath.Join(t.TempDir(), "source.format4.json")
	apply := newStateImportFormat4Command()
	apply.SetOut(new(bytes.Buffer))
	apply.SetArgs([]string{"--apply", "--offline", "--source-sha256", plan.SourceSHA256, "--archive", archive})
	require.NoError(t, apply.Execute())
	saved, err := os.ReadFile(archive)
	require.NoError(t, err)
	require.Equal(t, raw, saved)
	after, err := repo.LoadState(context.Background(), "lab")
	require.NoError(t, err)
	require.Equal(t, before, after)
}
