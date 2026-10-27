package commands

import (
	"context"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/persistence"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func loadWorkspaceWithRoot() (*graph.Graph, *state.Manager, *state.State, *config.Root, *hcl.EvalContext, error) {
	mgr, err := newManager()
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return runtime.LoadWorkspaceWithManager(flagConfigFile, mgr)
}

func loadWorkspace() (*graph.Graph, *state.Manager, *state.State, error) {
	g, mgr, s, _, _, err := loadWorkspaceWithRoot()
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
	backend, path, err := persistence.ResolveLocation(flagBackend, flagStateFile, localTopology())
	if err != nil {
		return nil, err
	}
	repo, err := persistence.OpenConfigured(context.Background(), backend, path)
	if err != nil {
		return nil, err
	}
	return state.NewManagerWithBackend(repo.StateBackend(localTopology())), nil
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
	if selected, err := persistence.ConfiguredTopology(flagBackend, ""); err == nil && selected != "" {
		return selected
	}
	dir := filepath.Base(filepath.Dir(flagStateFile))
	if dir == "." || dir == string(filepath.Separator) || dir == "" {
		return "local"
	}
	return dir
}
