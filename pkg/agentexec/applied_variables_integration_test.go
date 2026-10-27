package agentexec

import (
	"context"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPersistsOnlyDeclaredNonSensitiveVariablesIncludingNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "field.sysbox.hcl")
	require.NoError(t, os.WriteFile(path, []byte(`variable "mode" { default = "dev" }
variable "password" { sensitive = true }
`), 0600))
	bridge := NewLocalBridge(LocalOptions{Topology: "lab", ConfigFile: path, StatePath: filepath.Join(dir, "state.json"), RunsDir: dir, Log: io.Discard})
	for _, mode := range []string{"prod", "staging"} {
		run := &controlplane.Run{ID: mode, Topology: "lab", Op: "apply", Inputs: map[string]string{"mode": mode, "password": "plaintext-secret", "undeclared": "ignored"}}
		NewExecutorWithBridge(bridge).ExecuteContext(context.Background(), run)
		require.Equal(t, controlplane.RunDone, run.Status, run.Err)
		mgr, err := bridge.StateManager("lab")
		require.NoError(t, err)
		st, err := mgr.Load()
		require.NoError(t, err)
		require.Equal(t, map[string]string{"mode": mode}, st.AppliedVariables)
	}
}
