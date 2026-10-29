package workspace

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// beginGCProductionWrite records intent before any external mutation. Failed
// mutations deliberately leave updating=true; only the successful writer may
// complete. Contributions are cumulative because immutable revisions and
// content-addressed caches remain available to earlier runs.
func (s *Catalog) BeginGCProductionWrite(ctx context.Context, topology, kind string) (func(map[string][]string) error, error) {
	if s.GcSourceID == "" {
		if s.GcRepository != nil {
			repo, err := s.GcRepository(topology)
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
	if s.GcRepository == nil {
		return nil, fmt.Errorf("GC publication repository missing")
	}
	repo, err := s.GcRepository(topology)
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
	name := s.GcSourceID + "-" + kind
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
