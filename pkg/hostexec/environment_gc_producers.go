package hostexec

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// PrepareExecution is invoked by both ordinary and host-owned executors before
// preflight or effects; worker and host share this implementation.
func (b *Environment) PrepareExecution(ctx context.Context, run *controlplane.Run) (context.Context, func(), error) {
	if b.Cfg.GCPublicationSource == "" {
		if run == nil {
			return nil, nil, fmt.Errorf("run required")
		}
		_, err := b.Workspaces.BeginGCProductionWrite(ctx, run.Topology, "artifacts")
		if err != nil {
			return nil, nil, err
		}
		return ctx, func() {}, nil
	}
	if run == nil || run.Topology == "" {
		return nil, nil, fmt.Errorf("artifact publication requires topology")
	}
	return artifact.WithPublication(ctx, func(ctx context.Context, _ artifact.Spec) (func(artifact.Result) error, error) {
		complete, err := b.Workspaces.BeginGCProductionWrite(ctx, run.Topology, "artifacts")
		if err != nil {
			return nil, err
		}
		return func(result artifact.Result) error {
			return complete(map[string][]string{"artifact": {"sha256:" + result.SHA256}, "cache": {fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(result.Path)))}})
		}, nil
	}), func() {}, nil
}
