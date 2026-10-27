package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oslab/sysbox/pkg/api"
	"github.com/oslab/sysbox/pkg/config"
	"github.com/oslab/sysbox/pkg/controlplane"
	"github.com/oslab/sysbox/pkg/persistence"
)

// Called by LocalBridge only while holding the canonical topology lock.
func prepareLocalSSH(ctx context.Context, run *controlplane.Run) (context.Context, func(), error) {
	noop := func() {}
	backend, path, err := persistence.ResolveLocation(flagBackend, statePath(), run.Topology)
	if err != nil {
		return ctx, nil, err
	}
	repo, err := persistence.OpenConfigured(ctx, backend, path)
	if err != nil {
		return ctx, nil, err
	}
	defer repo.Close()
	st, err := repo.LoadState(ctx, run.Topology)
	if err != nil {
		return ctx, nil, err
	}
	needed := false
	for _, r := range st.Resources {
		needed = needed || r.Address.Type == "sysbox_ssh_access"
	}
	root, parseErr := config.ParseFile(flagConfigFile)
	if parseErr == nil {
		for _, r := range root.Resources {
			needed = needed || r.Type == "sysbox_ssh_access"
		}
	} else if run.Op != "destroy" && !os.IsNotExist(parseErr) {
		return ctx, nil, parseErr
	}
	if !needed {
		return ctx, noop, nil
	}
	cfg, err := config.LoadServiceConfig("")
	if err != nil {
		return ctx, nil, err
	}
	// Authority metadata must stay in the configured service directory. Deriving
	// a fresh ledger beside an arbitrary --state path could bypass grants issued
	// by an API using the same canonical state and signer.
	runs, err := filepath.Abs(cfg.Paths.RunsDir)
	if err != nil {
		return ctx, nil, err
	}
	if backend == "" {
		backend = path
	}
	if !strings.Contains(backend, "://") {
		backend, err = filepath.Abs(backend)
		if err != nil {
			return ctx, nil, err
		}
	}
	cfg.State.Backend = backend
	cfg.Paths.RunsDir = runs
	controller, err := api.NewLocalSSHController(ctx, cfg, run.Topology)
	if err != nil {
		return ctx, nil, fmt.Errorf("prepare local SSH authority: %w", err)
	}
	return controller.Context(ctx), func() { _ = controller.Close() }, nil
}
