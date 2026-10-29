package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"github.com/oslab/sysbox/pkg/artifact"
	"github.com/oslab/sysbox/pkg/controlplane"
)

// beginGCProductionWrite records intent before any external mutation. Failed
// mutations deliberately leave updating=true; only the successful writer may
// complete. Contributions are cumulative because immutable revisions and
// content-addressed caches remain available to earlier runs.
func (s *WorkspaceService) beginGCProductionWrite(ctx context.Context, topology, kind string) (func(map[string][]string) error, error) {
	if s.gcSourceID == "" {
		if s.gcRepository != nil {
			repo, err := s.gcRepository(topology)
			if err != nil {
				return nil, err
			}
			sources, err := repo.LoadGCSources(ctx, topology)
			if err != nil {
				return nil, err
			}
			if len(sources) > 0 {
				return nil, fmt.Errorf("enrolled topology requires gc_publication_source")
			}
		}
		return func(map[string][]string) error { return nil }, nil
	}
	if s.gcRepository == nil {
		return nil, fmt.Errorf("GC publication repository missing")
	}
	repo, err := s.gcRepository(topology)
	if err != nil {
		return nil, err
	}
	st, err := repo.LoadState(ctx, topology)
	if err != nil {
		return nil, err
	}
	sources, err := repo.LoadGCSources(ctx, topology)
	if err != nil {
		return nil, err
	}
	name := s.gcSourceID + "-" + kind
	previous := sources[name]
	version, err := repo.BeginGCSource(ctx, topology, name, st.TopologyUID, previous.Version)
	if err != nil {
		return nil, err
	}
	manifest := previous.Manifest
	if manifest.Domains == nil {
		manifest.Domains = map[string][]string{"revision": {}, "artifact": {}, "cache": {}, "placement": {}}
	}
	return func(contribution map[string][]string) error {
		for domain, values := range contribution {
			if _, ok := manifest.Domains[domain]; !ok {
				return fmt.Errorf("unknown GC domain %s", domain)
			}
			seen := map[string]bool{}
			for _, v := range append(manifest.Domains[domain], values...) {
				seen[v] = true
			}
			merged := make([]string, 0, len(seen))
			for v := range seen {
				merged = append(merged, v)
			}
			sort.Strings(merged)
			manifest.Domains[domain] = merged
		}
		manifest.ValidUntil = time.Now().UTC().Add(24 * time.Hour)
		return repo.CompleteGCSource(ctx, topology, name, st.TopologyUID, version, manifest)
	}, nil
}

// PrepareExecution is invoked by both ordinary and host-owned executors before
// preflight or effects; worker and host share this implementation.
func (b *ExecutionBridge) PrepareExecution(ctx context.Context, run *controlplane.Run) (context.Context, func(), error) {
	if b.cfg.GCPublicationSource == "" {
		if run == nil {
			return nil, nil, fmt.Errorf("run required")
		}
		_, err := b.workspaces.beginGCProductionWrite(ctx, run.Topology, "artifacts")
		if err != nil {
			return nil, nil, err
		}
		return ctx, func() {}, nil
	}
	if run == nil || run.Topology == "" {
		return nil, nil, fmt.Errorf("artifact publication requires topology")
	}
	return artifact.WithPublication(ctx, func(ctx context.Context, _ artifact.Spec) (func(artifact.Result) error, error) {
		complete, err := b.workspaces.beginGCProductionWrite(ctx, run.Topology, "artifacts")
		if err != nil {
			return nil, err
		}
		return func(result artifact.Result) error {
			return complete(map[string][]string{"artifact": {"sha256:" + result.SHA256}, "cache": {fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(result.Path)))}})
		}, nil
	}), func() {}, nil
}
