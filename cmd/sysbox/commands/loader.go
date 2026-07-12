package commands

import (
	"fmt"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func loadWorkspaceWithRoot() (*graph.Graph, *state.Manager, *state.State, *config.Root, *hcl.EvalContext, error) {
	return runtime.LoadWorkspace(flagConfigFile, statePath())
}

func loadWorkspace() (*graph.Graph, *state.Manager, *state.State, error) {
	g, mgr, s, _, _, err := runtime.LoadWorkspace(flagConfigFile, statePath())
	return g, mgr, s, err
}

// statePath returns the state file path or constructs a Manager with a
// custom backend when --backend is set.
func statePath() string {
	return flagStateFile
}

// newManager creates a state.Manager using the --backend flag if set,
// otherwise the plain --state path.
func newManager() (*state.Manager, error) {
	if flagBackend != "" {
		b, err := state.ParseBackendURL(flagBackend)
		if err != nil {
			return nil, fmt.Errorf("--backend: %w", err)
		}
		manager := state.NewManagerWithBackend(b)
		manager.AllowUnsafeMutation(flagAllowUnsafeState)
		return manager, nil
	}
	manager := state.NewManager(flagStateFile)
	manager.AllowUnsafeMutation(flagAllowUnsafeState)
	return manager, nil
}

func localRunsDir() string {
	dir := filepath.Dir(flagStateFile)
	if localTopology() == "local" {
		return dir
	}
	parent := filepath.Dir(dir)
	if parent == "." || parent == "" {
		return dir
	}
	return parent
}

func localTopology() string {
	dir := filepath.Base(filepath.Dir(flagStateFile))
	if dir == "." || dir == string(filepath.Separator) || dir == "" {
		return "local"
	}
	return dir
}
