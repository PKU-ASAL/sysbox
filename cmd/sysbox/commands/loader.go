package commands

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/graph"
	"github.com/oslab/sysbox/pkg/runtime"
	"github.com/oslab/sysbox/pkg/state"
)

func loadWorkspaceWithRoot() (*graph.Graph, *state.Manager, *state.State, *config.Root, *hcl.EvalContext, error) {
	return runtime.LoadWorkspace(flagConfigFile, flagStateFile)
}

func loadWorkspace() (*graph.Graph, *state.Manager, *state.State, error) {
	g, mgr, s, _, _, err := runtime.LoadWorkspace(flagConfigFile, flagStateFile)
	return g, mgr, s, err
}
