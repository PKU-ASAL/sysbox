package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"time"
)

// Format 7 is opt-in: old writers must refuse an enrolled aggregate rather
// than silently dropping or ignoring its external reference protections.
const GCSourceFormatVersion = 7

// GCSourceManifest is committed only after a participating writer finishes
// publication. Every external mutation must first call BeginGCSource.
// Expiry blocks new reservations; an existing reservation pins its complete
// manifest until a verified deletion receipt. Expiry never releases references.
type GCSourceManifest struct {
	Domains    map[string][]string   `json:"domains"`
	References []GCResourceReference `json:"references"`
	ValidUntil time.Time             `json:"valid_until"`
}
type GCSource struct {
	Retired  bool             `json:"retired,omitempty"`
	Version  int64            `json:"version"`
	Updating bool             `json:"updating"`
	Manifest GCSourceManifest `json:"manifest"`
}

func validateGCSourceManifest(m GCSourceManifest, owner string) error {
	if m.ValidUntil.IsZero() || len(m.References) > 4096 {
		return ErrInvalidRecord
	}
	if len(m.Domains) != 4 {
		return ErrInvalidRecord
	}
	for _, domain := range []string{"revision", "artifact", "cache", "placement"} {
		values, ok := m.Domains[domain]
		if !ok || len(values) > 4096 {
			return ErrInvalidRecord
		}
		for _, value := range values {
			if value == "" || len(value) > 4096 {
				return ErrInvalidRecord
			}
		}
	}
	seen := map[string]bool{}
	for _, ref := range m.References {
		if ref.Source == "" || len(ref.Source) > 4096 || seen[ref.Source] || ref.OwnerUID != owner || ref.ResourceUID == "" || ref.Generation < 1 || ref.ExternalID == "" {
			return ErrInvalidRecord
		}
		switch ref.Driver {
		case "docker", "libvirt", "firecracker":
		default:
			return ErrInvalidRecord
		}
		switch ref.ResourceType {
		case "sysbox_node", "sysbox_router", "sysbox_network":
		default:
			return ErrInvalidRecord
		}
		seen[ref.Source] = true
	}
	return nil
}
func validateGCSources(a *aggregate) error {
	if a.Format == FormatVersion {
		if a.GCSources != nil {
			return ErrCorruptRecord
		}
		return nil
	}
	if a.Format == GCDispositionFormatVersion && len(a.GCSources) == 0 {
		return nil
	}
	if (a.Format != GCSourceFormatVersion && a.Format != GCSourceRetirementFormatVersion && a.Format != GCDispositionFormatVersion) || a.GCSources == nil || len(a.GCSources) == 0 || len(a.GCSources) > 128 {
		return ErrCorruptRecord
	}
	for id, s := range a.GCSources {
		if s.Retired && ((a.Format != GCSourceRetirementFormatVersion && a.Format != GCDispositionFormatVersion) || s.Updating) {
			return ErrCorruptRecord
		}
		if !topologyName.MatchString(id) || len(id) > 128 || s.Version < 1 {
			return ErrCorruptRecord
		}
		if s.Updating && s.Manifest.Domains == nil {
			if s.Version != 1 || !reflect.DeepEqual(s.Manifest, GCSourceManifest{}) {
				return ErrCorruptRecord
			}
		}
		if !s.Updating || s.Manifest.Domains != nil {
			if validateGCSourceManifest(s.Manifest, a.State.TopologyUID) != nil {
				return ErrCorruptRecord
			}
		}
	}
	return nil
}

// BeginGCSource durably marks the source incomplete BEFORE an external writer
// may mutate/use references. Its CAS and GC reservation exclusion share the
// same backend transaction. A crashed writer leaves GC blocked indefinitely.
func (r *Repository) BeginGCSource(ctx context.Context, topology, source, owner string, expected int64) (int64, error) {
	if !topologyName.MatchString(source) || len(source) > 128 || expected < 0 || expected == math.MaxInt64 {
		return 0, ErrInvalidRecord
	}
	var version int64
	err := r.update(ctx, topology, func(a *aggregate) error {
		if owner != a.State.TopologyUID {
			return ErrConflict
		}
		current, exists := a.GCSources[source]
		if current.Retired || current.Version != expected || (exists && current.Updating) {
			return ErrConflict
		}
		if !exists && len(a.GCSources) >= 128 {
			return ErrConflict
		}
		if a.GCSources == nil {
			a.GCSources = map[string]GCSource{}
		}
		current.Version++
		current.Updating = true
		a.GCSources[source] = current
		if a.Format == FormatVersion {
			a.Format = GCSourceFormatVersion
		}
		version = current.Version
		return nil
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

func (r *Repository) CompleteGCSource(ctx context.Context, topology, source, owner string, expected int64, manifest GCSourceManifest) error {
	if expected < 1 || expected == math.MaxInt64 || validateGCSourceManifest(manifest, owner) != nil {
		return ErrInvalidRecord
	}
	// Detach caller-owned maps and slices before retaining data.
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	var detached GCSourceManifest
	if err = json.Unmarshal(raw, &detached); err != nil {
		return err
	}
	return r.update(ctx, topology, func(a *aggregate) error {
		if owner != a.State.TopologyUID {
			return ErrConflict
		}
		current, ok := a.GCSources[source]
		if !ok {
			return ErrNotFound
		}
		if current.Retired {
			return ErrConflict
		}
		if !current.Updating && current.Version == expected+1 && reflect.DeepEqual(current.Manifest, detached) {
			return nil
		}
		if !current.Updating || current.Version != expected || !detached.ValidUntil.After(time.Now()) {
			return ErrConflict
		}
		current.Version++
		current.Updating = false
		current.Manifest = detached
		a.GCSources[source] = current
		return nil
	})
}

func (r *Repository) LoadGCSources(ctx context.Context, topology string) (map[string]GCSource, error) {
	out := map[string]GCSource{}
	err := r.update(ctx, topology, func(a *aggregate) error {
		raw, err := json.Marshal(a.GCSources)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func collectGCSources(a *aggregate, current bool, prefix string, out *GCReferenceSnapshot, values map[string][]string, now time.Time) error {
	for id, source := range a.GCSources {
		if current && !source.Retired && (source.Updating || (!gcPending(a) && !source.Manifest.ValidUntil.After(now))) {
			return fmt.Errorf("%w: external source %s incomplete or expired", ErrConflict, id)
		}
		for domain, refs := range source.Manifest.Domains {
			values[domain] = append(values[domain], refs...)
		}
		for _, ref := range source.Manifest.References {
			ref.Source = prefix + "external/" + id + "/" + ref.Source
			out.References = append(out.References, ref)
		}
	}
	return nil
}
